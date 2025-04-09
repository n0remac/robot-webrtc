package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// ---------- DATA STRUCTURES ----------

var AllStances = []Stance{
	// 🟢 Supportive / Agreeing
	{Type: "supportive", SubType: "strong_agreement", Summary: "Full support, clear siding with OP."},
	{Type: "supportive", SubType: "qualified_agreement", Summary: "Mostly agrees but points out a small flaw."},
	{Type: "supportive", SubType: "empathetic_support", Summary: "Offers emotional validation and comfort."},
	{Type: "supportive", SubType: "personal_anecdote_support", Summary: "Shares a similar experience to affirm OP."},

	// 🔴 Opposing / Critical
	{Type: "opposing", SubType: "direct_opposition", Summary: "Strong disagreement with OP’s actions or views."},
	{Type: "opposing", SubType: "blame_shifting", Summary: "Redirects blame to OP even if they don't see it."},
	{Type: "opposing", SubType: "moral_critique", Summary: "Argues from ethical grounds against OP."},
	{Type: "opposing", SubType: "logical_critique", Summary: "Breaks down inconsistencies or irrationality."},
	{Type: "opposing", SubType: "assumes_missing_context", Summary: "Suggests OP left out key info to make themselves look better."},

	// ⚪ Neutral / Analytical
	{Type: "neutral", SubType: "dispassionate_analysis", Summary: "Lays out facts without judgment."},
	{Type: "neutral", SubType: "devils_advocate", Summary: "Takes a contrary position just to explore it."},
	{Type: "neutral", SubType: "both_sides", Summary: "Sees nuance and avoids strong alignment."},
	{Type: "neutral", SubType: "not_enough_info", Summary: "Requests clarification or additional details before weighing in."},
	{Type: "neutral", SubType: "legal_perspective", Summary: "Discusses legality rather than morality or emotions."},

	// 🟡 Complex / Mixed
	{Type: "mixed", SubType: "its_complicated", Summary: "Sees conflicting truths; not easily resolved."},
	{Type: "mixed", SubType: "everyone_at_fault", Summary: "Points to multiple parties being wrong."},
	{Type: "mixed", SubType: "no_one_at_fault", Summary: "Sees it as a tragic or inevitable situation."},
	{Type: "mixed", SubType: "consequentialist_view", Summary: "Focuses on outcomes, not intent."},
	{Type: "mixed", SubType: "cultural_context", Summary: "Cites how cultural norms affect judgment."},

	// 🟣 Narrative / Relational
	{Type: "narrative", SubType: "neutral_anecdote", Summary: "Shares experience without clear judgment."},
	{Type: "narrative", SubType: "projective_comment", Summary: "Relates deeply and interprets through their own lens."},
	{Type: "narrative", SubType: "advice_giver", Summary: "Offers next steps or solutions instead of judgment."},
	{Type: "narrative", SubType: "therapist_style", Summary: "Gently reframes the situation to promote self-awareness."},

	// 🔵 Meta / Humor / Offbeat
	{Type: "meta", SubType: "snarky", Summary: "Uses humor or irony to criticize."},
	{Type: "meta", SubType: "meme_comment", Summary: "Light-hearted or playful, not serious."},
	{Type: "meta", SubType: "call_out_subreddit", Summary: "Comments on how typical or cliché the post is."},
	{Type: "meta", SubType: "structure_commentary", Summary: "Critiques how the post is written or what it omits."},
}

// A single stance: e.g., "supportive", "strong_agreement", with a short summary
type Stance struct {
	Type    string `json:"type"`
	SubType string `json:"subtype"`
	Summary string `json:"summary"`
}

// The function-call response structure for stance selection
type StanceSelectionResponse struct {
	Stances []Stance `json:"stances"`
}

// Each user gets a RedditSession
type RedditSession struct {
	ID              string
	Prompt          string
	Subreddit       string
	SelectedStances []Stance // The stances chosen by GPT
	Responses       []SimulatedComment
	Done            bool
	Error           error
}

// Comment-style response from a Reddit simulation
type SimulatedComment struct {
	Username string
	Flair    string
	Text     string
	Replies  []SimulatedComment
}

// Session store (in-memory for now)
var (
	redditSessions      = make(map[string]*RedditSession)
	redditSessionsMutex sync.Mutex
)

// ---------- MAIN + ROUTES ----------

func ShadowReddit(mux *http.ServeMux) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	mux.HandleFunc("/shadowreddit", ServeNode(RedditHomePage()))
	mux.HandleFunc("/shadowreddit/new", ServeNode(RedditPromptPage()))

	mux.HandleFunc("/shadowreddit/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Error parsing form", http.StatusBadRequest)
			return
		}
		prompt := r.FormValue("prompt")
		subreddit := r.FormValue("subreddit")
		if prompt == "" {
			http.Error(w, "Prompt cannot be empty", http.StatusBadRequest)
			return
		}

		// Create and store the session
		session := NewSession(prompt, subreddit)
		log.Printf("[INFO] Created session %s", session.ID)

		// Kick off AI work in background goroutine
		go func(sess *RedditSession) {
			var wg sync.WaitGroup

			// 1) Get stances from GPT
			selectedStances, err := generateStances(client, subreddit, prompt)
			if err != nil {
				log.Printf("[ERROR] generating stances: %v", err)
				sess.Error = err
				sess.Done = true
				return
			}

			// 2) Store stances in the session
			redditSessionsMutex.Lock()
			sess.SelectedStances = selectedStances
			redditSessionsMutex.Unlock()

			// 3) For each stance, generate a single top-level comment
			for _, stance := range selectedStances {
				text, err := GenerateResponseFromStance(client, prompt, stance)
				if err != nil {
					log.Printf("[ERROR] generating response: %v", err)
					sess.Error = err
					break
				}

				// Build the top-level comment
				comment := SimulatedComment{
					Username: fmt.Sprintf("%s_%s", stance.Type, stance.SubType),
					Flair:    stance.Type,
					Text:     text,
				}

				// Append to session and get its index
				redditSessionsMutex.Lock()
				idx := len(sess.Responses)
				sess.Responses = append(sess.Responses, comment)
				redditSessionsMutex.Unlock()

				// Spawn a goroutine to generate a reply for THIS top-level comment
				wg.Add(1)
				go func(parentIndex int, parentText string) {
					defer wg.Done()

					replyText, err := GenerateReplyToComment(client, sess.Prompt, parentText)
					if err != nil {
						log.Printf("[ERROR] generating reply: %v", err)
						// We'll just log the error. We won't stop the entire session.
						return
					}

					child := SimulatedComment{
						Username: randomReplyUsername(),
						Flair:    "reply",
						Text:     replyText,
					}

					redditSessionsMutex.Lock()
					sess.Responses[parentIndex].Replies = append(sess.Responses[parentIndex].Replies, child)
					redditSessionsMutex.Unlock()
				}(idx, text)
			}

			// 4) Once ALL replies are done, mark the session done
			go func() {
				wg.Wait()
				sess.Done = true
			}()
		}(session)

		http.Redirect(w, r, "/shadowreddit/session?id="+session.ID, http.StatusSeeOther)
	})

	mux.HandleFunc("/shadowreddit/session", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		session, ok := GetSession(id)
		if !ok {
			http.Error(w, "Invalid session ID", http.StatusNotFound)
			return
		}
		ServeNode(RedditSessionPage(session.Prompt, session.ID))(w, r)
	})

	mux.HandleFunc("/shadowreddit/ws", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		sess, ok := GetSession(id)
		if !ok {
			http.Error(w, "Invalid session", http.StatusNotFound)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade error: %v", err)
			return
		}
		defer conn.Close()

		log.Printf("WebSocket connected for session %s", id)

		lastSentTopLevel := 0
		replyCounts := make([]int, 0)

		// In your loop setup, you might do:
		redditSessionsMutex.Lock()
		replyCounts = make([]int, len(sess.Responses))
		redditSessionsMutex.Unlock()

		for {
			redditSessionsMutex.Lock()
			done := sess.Done

			// 1) Check if any new top-level comments arrived
			for lastSentTopLevel < len(sess.Responses) {
				comment := sess.Responses[lastSentTopLevel]
				html := RenderCommentRecursive(comment, 0).Render()
				conn.WriteJSON(map[string]string{
					"type":        "comment",
					"parentIndex": fmt.Sprintf("%d", lastSentTopLevel),
					"html":        html,
				})
				lastSentTopLevel++
				replyCounts = append(replyCounts, len(comment.Replies))
			}

			// 2) Check each existing comment for new replies
			for i, comment := range sess.Responses {
				newReplyCount := len(comment.Replies)
				if newReplyCount > replyCounts[i] {
					// We have new replies
					for r := replyCounts[i]; r < newReplyCount; r++ {
						singleReply := comment.Replies[r]
						replyHTML := RenderCommentRecursive(singleReply, 1).Render()
						// We'll also send info about which parent index or comment ID to attach to
						conn.WriteJSON(map[string]string{
							"type":        "reply",
							"parentIndex": fmt.Sprintf("%d", i),
							"html":        replyHTML,
						})
					}
					replyCounts[i] = newReplyCount
				}
			}

			redditSessionsMutex.Unlock()

			if done {
				conn.WriteJSON(map[string]string{"type": "done"})
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	})
}

// ---------- LAYOUT / TEMPLATES ----------

// Home page
func RedditHomePage() *Node {
	return DefaultLayout(
		Div(Class("container mx-auto p-8 text-center space-y-4"),
			H1(Class("text-3xl font-bold"), T("Welcome to the Reddit Simulation Tool")),
			P(Class("text-lg"),
				T("This app helps you reflect on complex emotional situations by simulating a Reddit thread with multiple perspectives."),
			),
			A(Href("/shadowreddit/new"),
				Class("inline-block mt-4 text-blue-600 hover:underline"),
				T("Start a New Post"),
			),
		),
		Footer(
			Class("text-center text-sm text-gray-500"),
			T("ShadowReddit is not affiliated with Reddit in anyway."),
		),
	)
}

// Page for user input
func RedditPromptPage() *Node {
	return DefaultLayout(
		Main(Class("max-w-2xl mx-auto p-8 space-y-6"),
			H1(Class("text-2xl font-bold"), T("ShadowReddit")),
			Form(Method("POST"), Action("/shadowreddit/start"),
				Div(Class("mb-4"),
					Label(For("prompt"), Class("block font-medium mb-1"), T("Your Problem (Reddit-style post)")),
					TextArea(Id("prompt"), Name("prompt"), Class("w-full border rounded p-2"), Rows(6)),
				),
				Div(Class("mb-4"),
					Label(For("subreddit"), Class("block font-medium mb-1"), T("Simulated Subreddit")),
					Select(Name("subreddit"), Id("subreddit"), Class("w-full border rounded p-2"),
						Option(Value("aita"), T("r/AmITheAsshole")),
						Option(Value("relationships"), T("r/relationships")),
						Option(Value("legaladvice"), T("r/legaladvice")),
						Option(Value("askreddit"), T("r/AskReddit")),
					),
				),
				Button(Type("submit"), Class("bg-blue-600 text-white px-4 py-2 rounded"), T("Simulate Responses")),
			),
		),
	)
}

// RenderCommentRecursive renders a single comment, then any child replies.
// 'indentLevel' tells us how far to indent for nested replies.
func RenderCommentRecursive(c SimulatedComment, indentLevel int) *Node {
	indentClass := fmt.Sprintf("ml-%d", indentLevel*6) // or any indentation you like

	// Render this comment
	mainComment := Div(Class(fmt.Sprintf("bg-white p-4 rounded shadow mb-4 %s", indentClass)),
		Div(Class("flex items-center justify-between"),
			Span(Class("font-semibold text-blue-700"), Text(c.Username)),
			Span(Class("text-sm text-gray-500"), Text(c.Flair)),
		),
		P(Class("mt-2 text-gray-800"), Text(c.Text)),
	)

	// If no replies, just return
	if len(c.Replies) == 0 {
		return mainComment
	}

	// Container for nested replies
	replyNodes := []*Node{mainComment}
	for _, child := range c.Replies {
		// Recursively render each child, incrementing indent
		childNode := RenderCommentRecursive(child, indentLevel+1)
		replyNodes = append(replyNodes, childNode)
	}

	// Combine this comment + all children
	return Div(replyNodes...)
}

// Page that displays the simulated responses
func RedditSessionPage(prompt string, sessionID string) *Node {
	return DefaultLayout(
		Div(Class("max-w-2xl mx-auto p-6 space-y-6"),
			H1(Class("text-2xl font-bold"), T("Your Reddit Simulation")),
			Div(Class("bg-gray-100 p-4 rounded"),
				H2(Class("font-semibold text-lg"), T("Your Post")),
				P(Class("mt-2 whitespace-pre-wrap text-gray-800"), Text(prompt)),
			),
			Div(Id("responseArea"),
				P(Class("text-gray-500 italic"), T("Generating simulated responses...")),
				Div(Class("mt-2"),
					Progress(Class("progress progress-primary w-full"), Max("100")),
				),
			),
			Script(Raw(fmt.Sprintf(`
	const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
	let ws = new WebSocket(protocol + "//" + window.location.host + "/shadowreddit/ws?id=%s");
	let responseArea = document.getElementById("responseArea");

	ws.onmessage = function(event) {
		let data = JSON.parse(event.data);

		if (data.type === "comment") {
			// Create a container for this top-level comment
			let parentDiv = document.createElement("div");
			parentDiv.setAttribute("id", "comment-" + data.parentIndex);
			parentDiv.innerHTML = data.html;
			responseArea.appendChild(parentDiv);

		} else if (data.type === "reply") {
			// Append a reply to an existing comment's container
			let parentDiv = document.getElementById("comment-" + data.parentIndex);
			if (!parentDiv) {
				console.warn("No parent container found for index", data.parentIndex);
				return;
			}
			let replyDiv = document.createElement("div");
			replyDiv.innerHTML = data.html;
			parentDiv.appendChild(replyDiv);

		} else if (data.type === "done") {
			// Signal that simulation is complete
			let p = document.createElement("p");
			p.innerText = "Simulation complete.";
			responseArea.appendChild(p);
			ws.close();
		}
	};
`, sessionID))),
		),
	)
}

// ---------- HELPER FUNCTIONS ----------

// Creates a new session
func NewSession(prompt, subreddit string) *RedditSession {
	id := randomID()
	s := &RedditSession{
		ID:        id,
		Prompt:    prompt,
		Subreddit: subreddit,
	}
	redditSessionsMutex.Lock()
	redditSessions[id] = s
	redditSessionsMutex.Unlock()
	return s
}

// Retrieves a session by ID
func GetSession(id string) (*RedditSession, bool) {
	redditSessionsMutex.Lock()
	defer redditSessionsMutex.Unlock()
	s, ok := redditSessions[id]
	return s, ok
}

func randomReplyUsername() string {
	names := []string{
		"ReplyMaster",
		"CuriousCat",
		"HonestAbe",
		"DebateKing",
		"FriendlyNeighbor",
		"JustSaying",
		"RandomUser",
		"WittyRemark",
		"SkepticalSam",
		"AgreeableAlex",
	}
	return names[rand.Intn(len(names))]
}

// ---------- AI FUNCTIONS ----------

// generateStances picks 5-8 stances from AllStances using GPT's function-calling
func generateStances(client *openai.Client, thread string, post string) ([]Stance, error) {
	// Create a JSON-safe string version of AllStances to pass to GPT
	allStancesJSON, err := json.Marshal(AllStances)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal AllStances: %w", err)
	}

	systemPrompt := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleSystem,
		Content: `You are helping choose a set of stances for a Reddit thread.
Select 5 to 8 stances from a given list of predefined options. Choose perspectives that would likely be given. Do not invent new stances.
Use only stances from the provided list. It is ok if stances are repeated.`,
	}

	userMessage := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		Content: fmt.Sprintf(`Reddit Thread Title: %s
Post Content: %s

Here is the full list of allowed stances (with type, subtype, and summary):
%s`, thread, post, string(allStancesJSON)),
	}

	fn := openai.FunctionDefinition{
		Name:        "select_stances",
		Description: "Select 5 to 8 stances from a list of predefined options",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stances": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":    map[string]any{"type": "string"},
							"subtype": map[string]any{"type": "string"},
							"summary": map[string]any{"type": "string"},
						},
						"required": []string{"type", "subtype", "summary"},
					},
				},
			},
			"required": []string{"stances"},
		},
	}

	chatRequest := openai.ChatCompletionRequest{
		Model: "gpt-4-0613",
		Messages: []openai.ChatCompletionMessage{
			systemPrompt,
			userMessage,
		},
		Functions:    []openai.FunctionDefinition{fn},
		FunctionCall: openai.FunctionCall{Name: "select_stances"},
	}

	chatResp, err := client.CreateChatCompletion(context.Background(), chatRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to get response from OpenAI: %w", err)
	}

	choice := chatResp.Choices[0]
	if choice.Message.FunctionCall == nil {
		return nil, fmt.Errorf("no function call in OpenAI response")
	}

	var parsed StanceSelectionResponse
	err = json.Unmarshal([]byte(choice.Message.FunctionCall.Arguments), &parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal function response: %w", err)
	}

	return parsed.Stances, nil
}

// GenerateResponseFromStance creates a single Reddit comment from a stance + user prompt
func GenerateResponseFromStance(client *openai.Client, prompt string, stance Stance) (string, error) {
	systemMsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleSystem,
		Content: fmt.Sprintf(
			`You are a Reddit commenter who holds the following stance:
Type: %s
SubType: %s
Summary: %s

Write a single Reddit comment responding to the user's post from this perspective.
Your response should sound like a typical Reddit user with that viewpoint.
`,
			stance.Type, stance.SubType, stance.Summary,
		),
	}

	userMsg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: fmt.Sprintf("Here is the Reddit post:\n%s", prompt),
	}

	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    openai.GPT4,
			Messages: []openai.ChatCompletionMessage{systemMsg, userMsg},
		},
	)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return resp.Choices[0].Message.Content, nil
}

func GenerateReplyToComment(client *openai.Client, originalPost, parentComment string) (string, error) {
	systemMsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleSystem,
		Content: `You are simulating a reply in a Reddit thread. 
        You have the original post and a parent comment. 
        Write a single reply as if you are another Reddit user. 
        Keep it natural and typical of Reddit discussions.`,
	}

	userMsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		Content: fmt.Sprintf(`ORIGINAL POST:
%s

PARENT COMMENT:
%s

Please write a single short reply to the parent comment.`, originalPost, parentComment),
	}

	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:    openai.GPT4,
		Messages: []openai.ChatCompletionMessage{systemMsg, userMsg},
	})
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}
	return resp.Choices[0].Message.Content, nil
}
