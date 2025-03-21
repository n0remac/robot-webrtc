package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sashabaranov/go-openai"
)

// -------------------------------------------------------
// Models & Data Structures
// -------------------------------------------------------

// Book: overall structure from the function call
type Book struct {
	Title string          `json:"title"`
	Pages map[string]Page `json:"pages"`
}

// Page: text + image desc
type Page struct {
	Text             string `json:"text"`
	ImageDescription string `json:"image_description"`
}

// PagesArray used for fallback if the model returns an array
type PagesArray []Page

func tryUnmarshalPages(data []byte) (map[string]Page, error) {
	obj := make(map[string]Page)
	if err := json.Unmarshal(data, &obj); err == nil {
		return obj, nil
	}
	var arr PagesArray
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("pages is not an object nor an array")
	}
	obj = make(map[string]Page)
	for i, p := range arr {
		obj[strconv.Itoa(i+1)] = p
	}
	return obj, nil
}

// createBookFn: function definition for function calling
var createBookFn = openai.FunctionDefinition{
	Name:        "create_book_json",
	Description: "Generate a JSON structure for a children's-style book with a title, where each page has text and an image description",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "The title of the book",
			},
			"pages": map[string]interface{}{
				"type":        "object",
				"description": "An object with numeric keys. Each key has {text, image_description}",
				"patternProperties": map[string]interface{}{
					"^[0-9]+$": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"text": map[string]interface{}{
								"type": "string",
							},
							"image_description": map[string]interface{}{
								"type": "string",
							},
						},
						"required": []string{"text", "image_description"},
					},
				},
			},
		},
		"required": []string{"title", "pages"},
	},
}

// Session holds data about a single generation
type Session struct {
	ID          string
	Title       string
	TotalPages  int
	CurrentPage int
	BookHTML    string
	Error       error
	Done        bool
	Mutex       sync.Mutex

	// We'll store the *websocket.Conn so we can push messages
	Conn *websocket.Conn
}

// BookPromptData used by the prompt page template
type BookPromptData struct {
	DefaultPrompt string
	RandomPrompts []string
}

var (
	sessions      = make(map[string]*Session)
	sessionsMutex sync.Mutex
)

// The prompt page template
var promptTmpl = template.Must(template.New("promptPage").Parse(`
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>Generate a Book</title>
</head>
<body style="font-family: sans-serif;">
  <h1>Generate a Custom Children's Book</h1>
  <p>Enter a brief idea or theme for your story, or try one of the random prompts below.</p>
  <form action="/story/generate" method="POST">
    <label for="userPrompt">Your Book Idea:</label><br>
    <textarea name="userPrompt" id="userPrompt" rows="4" cols="50">{{.DefaultPrompt}}</textarea><br><br>
    <input type="submit" value="Generate Book">
  </form>

  <h2>Random Prompt Ideas</h2>
  <ul>
    {{range .RandomPrompts}}
      <li>{{.}}</li>
    {{end}}
  </ul>
  <p>(Copy/paste one of these into the text area if you like!)</p>
</body>
</html>
`))

// -------------------------------------------------------
// MAIN
// -------------------------------------------------------

func GenerateStory() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	if err := os.MkdirAll("books", 0755); err != nil {
		log.Fatalf("Cannot create 'books' directory: %v", err)
	}

	http.HandleFunc("/story", handlePromptPage)

	// POST /generate
	http.HandleFunc("/story/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		userPrompt := r.FormValue("userPrompt")
		if userPrompt == "" {
			http.Error(w, "No prompt provided", http.StatusBadRequest)
			return
		}
		sessionID := randomID()
		session := &Session{ID: sessionID}
		sessionsMutex.Lock()
		sessions[sessionID] = session
		sessionsMutex.Unlock()

		log.Printf("[INFO] Created session %s for prompt %q", sessionID, userPrompt)
		// We'll spawn the generation later after the user connects the WebSocket
		// or you can spawn it now if you want, but ideally we wait for the user to connect
		// so we can push progress from the start.

		// For simplicity, let's just spawn it now:
		go generateBook(session, userPrompt, client)

		// Then redirect to /loading?id=<sessionID>
		http.Redirect(w, r, "/story/loading?id="+sessionID, http.StatusSeeOther)
	})

	// GET /loading -> returns HTML with JavaScript that opens a WebSocket to /ws
	http.HandleFunc("/story/loading", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// In the JavaScript below, we open a WebSocket: /ws?id=<sessionID>
		// We'll receive JSON messages from the server about progress.
		loadingPage := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>Loading %s</title>
</head>
<body style="font-family: sans-serif;">
  <h1>Generating your book...</h1>
  <div id="progressArea">Waiting for updates...</div>
  <script>
    // Use wss:// if the page is loaded over HTTPS, otherwise use ws://
    let protocol = window.location.protocol === "https:" ? "wss://" : "ws://";
    let ws = new WebSocket(protocol + window.location.host + "/story/ws?id=%s");
    let progressEl = document.getElementById("progressArea");

    ws.onopen = function(event) {
      console.log("WebSocket connected for session=%s");
    };

    ws.onmessage = function(event) {
      let data = JSON.parse(event.data);
      if (data.type === "progress") {
        // Use separate elements so each line is on its own line
        let html = "<div>Title: " + data.title + "</div>" +
                   "<div>Generating page " + data.currentPage + " of " + data.totalPages + "</div>";
        progressEl.innerHTML = html;
      } else if (data.type === "done") {
        let linkUrl = "https://" + window.location.host + "/" + data.bookHTML;
        let html = "<p>All pages generated!</p>" +
                   "<p>Open your new book here: <a href='" + linkUrl + "'>" + linkUrl + "</a></p>";
        progressEl.innerHTML = html;
        ws.close();
      } else if (data.type === "error") {
        progressEl.innerHTML = "<p style='color:red'>Error: " + data.error + "</p>";
        ws.close();
      }
    };

    ws.onerror = function(event) {
      progressEl.innerHTML = "<p style='color:red'>WebSocket error!</p>";
    };

    ws.onclose = function(event) {
      console.log("WebSocket closed for session=%s");
    };
  </script>
</body>
</html>
`, sessionID, sessionID, sessionID, sessionID)
		fmt.Fprint(w, loadingPage)
	})

	// GET /ws -> upgrade to WebSocket
	http.HandleFunc("/story/ws", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		sessionsMutex.Lock()
		session, ok := sessions[sessionID]
		sessionsMutex.Unlock()
		if !ok {
			http.Error(w, "Invalid session ID", http.StatusBadRequest)
			return
		}

		// Upgrade
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ERROR] Upgrading to WS: %v", err)
			return
		}
		log.Printf("[INFO] WebSocket connection established for session=%s", sessionID)

		// store the WebSocket connection
		session.Mutex.Lock()
		session.Conn = conn
		// If generation is "already done," send a final update right away.
		if session.Done {
			if session.Error != nil {
				sendWebSocketError(session, session.Error)
				conn.Close()
			} else {
				sendWebSocketDone(session)
				conn.Close()
			}
		}
		session.Mutex.Unlock()

		// Optionally handle read messages in a loop if you need them
		// but for now, we only push from server to client
	})

	// Serve generated files from /books
	http.Handle("/books/", http.StripPrefix("/books/", http.FileServer(http.Dir("books"))))

	log.Println("[INFO] Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// -------------------------------------------------------
// Generation Logic
// -------------------------------------------------------

func generateBook(session *Session, userPrompt string, client *openai.Client) {
	log.Printf("[INFO] Starting generation for session=%s, prompt=%q", session.ID, userPrompt)

	// 1. Chat request to get JSON
	fn := openai.FunctionDefinition{
		Name:        "create_book_json",
		Description: "Generate a JSON structure for a children's-style book with a title, where each page has text and an image description",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "The title of the book",
				},
				"pages": map[string]interface{}{
					"type":        "object",
					"description": "An object where each key is a page number, and the value has text and an image_description",
					// patternProperties requires a map describing the pattern for each numeric key
					"patternProperties": map[string]interface{}{
						"^[0-9]+$": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"text": map[string]interface{}{
									"type":        "string",
									"description": "The text content of the page",
								},
								"image_description": map[string]interface{}{
									"type":        "string",
									"description": "A short description of what the illustration should show",
								},
							},
							"required": []string{"text", "image_description"},
						},
					},
				},
			},
			"required": []string{"title", "pages"},
		},
	}

	// We'll craft a system & user message for the Chat completion
	systemMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are a creative children's book generator. You produce short books with a title and pages. Each page has text and a short image_description for illustration. Return your answer only by calling the function 'create_book_json' with the correct JSON structure. Use a whimsical style.",
	}
	userMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userPrompt,
	}

	// Perform the ChatCompletion with function calling
	resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model: "gpt-4-0613",
		Messages: []openai.ChatCompletionMessage{
			systemMessage,
			userMessage,
		},
		Functions:    []openai.FunctionDefinition{fn},
		FunctionCall: openai.FunctionCall{Name: fn.Name},
	})
	if err != nil {
		log.Fatalf("Error calling OpenAI: %v", err)
	}
	if len(resp.Choices) == 0 {
		log.Fatal("No response from OpenAI")
	}
	session.Mutex.Lock()
	if err != nil {
		session.Error = fmt.Errorf("OpenAI error: %v", err)
		session.Mutex.Unlock()
		sendWebSocketError(session, session.Error)
		return
	}
	if len(resp.Choices) == 0 {
		session.Error = fmt.Errorf("No response from OpenAI")
		session.Mutex.Unlock()
		sendWebSocketError(session, session.Error)
		return
	}
	fnCall := resp.Choices[0].Message.FunctionCall
	if fnCall == nil {
		session.Error = fmt.Errorf("No function call from model")
		session.Mutex.Unlock()
		sendWebSocketError(session, session.Error)
		return
	}
	session.Mutex.Unlock()

	// 2. Parse JSON
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fnCall.Arguments), &raw); err != nil {
		sendWebSocketError(session, fmt.Errorf("Error unmarshaling arguments: %v", err))
		return
	}

	var title string
	if err := json.Unmarshal(raw["title"], &title); err != nil {
		sendWebSocketError(session, fmt.Errorf("Error unmarshaling title: %v", err))
		return
	}
	pages, err := tryUnmarshalPages(raw["pages"])
	if err != nil {
		sendWebSocketError(session, fmt.Errorf("Error unmarshaling pages: %v", err))
		return
	}

	book := Book{Title: title, Pages: pages}
	session.Mutex.Lock()
	session.Title = book.Title
	session.TotalPages = len(pages)
	session.CurrentPage = 0
	session.Mutex.Unlock()

	if session.TotalPages == 0 {
		// finalize empty
		log.Printf("[INFO] session=%s has 0 pages, finishing empty", session.ID)
		finalizeEmptyBook(session, book)
		sendWebSocketDone(session)
		return
	}

	// 3. Save JSON, then generate HTML with images
	timestamp := time.Now().Unix()
	jsonPath := fmt.Sprintf("books/%d.json", timestamp)
	htmlPath := fmt.Sprintf("books/%d.html", timestamp)

	// Save the JSON
	if err := saveBookJSON(jsonPath, book); err != nil {
		sendWebSocketError(session, err)
		return
	}

	// Sort pages by numeric key
	var pageNums []int
	for k := range pages {
		if n, e := strconv.Atoi(k); e == nil {
			pageNums = append(pageNums, n)
		}
	}
	sort.Ints(pageNums)

	hf, err := os.Create(htmlPath)
	if err != nil {
		sendWebSocketError(session, fmt.Errorf("Cannot create HTML file: %v", err))
		return
	}
	defer hf.Close()

	// Write base HTML
	header := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>%s</title>
  <style>
    body {
      font-family: sans-serif;
      margin: 0;
      padding: 0;
      text-align: center;
      background: #FAF9F6;
    }
    .book-page {
      display: none;
      width: 80%%;
      margin: 0 auto;
      max-width: 600px;
      padding: 2em;
      background: #FFFFFF;
      border: 1px solid #DDD;
      margin-top: 2em;
      box-shadow: 0 2px 5px rgba(0,0,0,0.2);
    }
    .book-page img {
      max-width: 100%%;
      height: auto;
      display: block;
      margin: 1em auto;
    }
    #title-page {
      display: block;
      margin-top: 5em;
    }
    #nav-buttons {
      margin: 2em;
    }
    button {
      padding: 0.5em 1em;
      cursor: pointer;
      font-size: 1em;
    }
  </style>
  <script>
    let currentPage = 0;
    let totalPages = 0;

    function showPage(index) {
      const oldPage = document.getElementById('page-' + currentPage);
      if (oldPage) oldPage.style.display = 'none';

      const titlePage = document.getElementById('title-page');
      if (currentPage === 0 && titlePage) {
        titlePage.style.display = 'none';
      }

      currentPage = index;
      if (currentPage === 0 && titlePage) {
        titlePage.style.display = 'block';
        return;
      }

      const newPage = document.getElementById('page-' + currentPage);
      if (newPage) {
        newPage.style.display = 'block';
      }
    }

    function nextPage() {
      if (currentPage < totalPages) {
        showPage(currentPage + 1);
      }
    }

    function prevPage() {
      if (currentPage > 0) {
        showPage(currentPage - 1);
      }
    }

    window.onload = function() {
      showPage(0);
    };
  </script>
</head>
<body>
`, book.Title)
	hf.WriteString(header)

	titlePageHTML := fmt.Sprintf(`
<div id="title-page">
  <h1>%s</h1>
  <p>Enjoy your custom story!</p>
</div>
`, book.Title)
	hf.WriteString(titlePageHTML)

	// 4. For each page, generate an image
	for i, pageNum := range pageNums {
		strPage := strconv.Itoa(pageNum)
		pg := pages[strPage]

		// increment progress
		session.Mutex.Lock()
		session.CurrentPage = i + 1
		sendWebSocketProgress(session) // push a "progress" update to WS
		session.Mutex.Unlock()

		prompt := fmt.Sprintf("Children's book style illustration: %s. No text in the image.", pg.ImageDescription)
		imgResp, err := client.CreateImage(context.Background(), openai.ImageRequest{
			Prompt: prompt,
			N:      1,
			Size:   "512x512",
		})
		if err != nil {
			sendWebSocketError(session, fmt.Errorf("error generating image for page %s: %v", strPage, err))
			return
		}
		if len(imgResp.Data) == 0 {
			sendWebSocketError(session, fmt.Errorf("no image data returned for page %s", strPage))
			return
		}
		imageURL := imgResp.Data[0].URL
		res, err := http.Get(imageURL)
		if err != nil {
			sendWebSocketError(session, fmt.Errorf("error downloading image from %s: %v", imageURL, err))
			return
		}
		imgBytes, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			sendWebSocketError(session, fmt.Errorf("error reading image for page %s: %v", strPage, err))
			return
		}
		encoded := base64.StdEncoding.EncodeToString(imgBytes)
		dataURL := fmt.Sprintf("data:image/png;base64,%s", encoded)

		pageDivID := i + 1
		pageHTML := fmt.Sprintf(`<div class="book-page" id="page-%d">
<p>%s</p>
<img src="%s" alt="Page Illustration">
</div>
`, pageDivID, pg.Text, dataURL)
		hf.WriteString(pageHTML)
	}

	navHTML := `
<div id="nav-buttons">
  <button onclick="prevPage()">Previous Page</button>
  <button onclick="nextPage()">Next Page</button>
</div>
`
	hf.WriteString(navHTML)

	fmt.Fprintf(hf, `
<script>
  totalPages = %d;
</script>
</body></html>
`, len(pageNums))
	hf.Close()

	// Mark done
	session.Mutex.Lock()
	session.BookHTML = htmlPath
	session.Done = true
	session.Mutex.Unlock()

	sendWebSocketDone(session) // final message
}

// If no pages
func finalizeEmptyBook(session *Session, book Book) {
	timestamp := time.Now().Unix()
	jsonPath := fmt.Sprintf("books/%d.json", timestamp)
	htmlPath := fmt.Sprintf("books/%d.html", timestamp)

	if err := saveBookJSON(jsonPath, book); err != nil {
		sendWebSocketError(session, err)
		return
	}
	hf, err := os.Create(htmlPath)
	if err != nil {
		sendWebSocketError(session, err)
		return
	}
	fmt.Fprintf(hf, `<!DOCTYPE html>
<html><head><meta charset="UTF-8">
<title>%s</title></head>
<body style="font-family:sans-serif;text-align:center;">
<h1>%s</h1>
<p>No pages generated.</p>
</body></html>
`, book.Title, book.Title)
	hf.Close()

	session.Mutex.Lock()
	session.BookHTML = htmlPath
	session.Done = true
	session.Mutex.Unlock()
}

// saveBookJSON writes the Book to a JSON file
func saveBookJSON(path string, b Book) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

// randomID returns a random 12-char ID
func randomID() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 12)
	rand.Seed(time.Now().UnixNano())
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// -------------------------------------------------------
// WebSocket Helper Functions
// -------------------------------------------------------

// sendWebSocketProgress: writes a JSON message of type=progress
func sendWebSocketProgress(s *Session) {
	if s.Conn == nil {
		return
	}
	msg := map[string]interface{}{
		"type":        "progress",
		"title":       s.Title,
		"currentPage": s.CurrentPage,
		"totalPages":  s.TotalPages,
	}
	if err := s.Conn.WriteJSON(msg); err != nil {
		log.Printf("[WARN] session=%s sending progress error: %v", s.ID, err)
		s.Conn.Close()
		s.Conn = nil
	}
}

// sendWebSocketDone: writes a JSON message of type=done
func sendWebSocketDone(s *Session) {
	if s.Conn == nil {
		return
	}
	msg := map[string]interface{}{
		"type":     "done",
		"bookHTML": s.BookHTML,
	}
	if err := s.Conn.WriteJSON(msg); err != nil {
		log.Printf("[WARN] session=%s sending done error: %v", s.ID, err)
		s.Conn.Close()
		s.Conn = nil
	}
}

// sendWebSocketError: sets the session error, writes a JSON error message
func sendWebSocketError(s *Session, err error) {
	s.Mutex.Lock()
	s.Error = err
	s.Done = true
	s.Mutex.Unlock()

	if s.Conn != nil {
		msg := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}
		if e := s.Conn.WriteJSON(msg); e != nil {
			log.Printf("[WARN] session=%s sending error message: %v", s.ID, e)
		}
		s.Conn.Close()
		s.Conn = nil
	}
	log.Printf("[ERROR] session=%s -> %v", s.ID, err)
}

// -------------------------------------------------------
// handlePromptPage just renders the form
// -------------------------------------------------------
func handlePromptPage(w http.ResponseWriter, r *http.Request) {
	randomPrompts := []string{
		"A curious Python exploring the magical land of code.",
		"A JavaScript knight on a quest to debug a dragon.",
		"The adventure of Sir Recursion in the Looping Kingdom.",
		"A brave coder who learns the secret of inheritance.",
		"A little function that calls itself and grows into a recursion master.",
		"The tale of the missing semicolon in the Kingdom of Syntax.",
		"A tiny variable on a journey to find its scope.",
		"The debugging adventures of Captain Compile in Code Land.",
		"A lonely array that discovers the beauty of a slice.",
		"The secret life of a function that loves closures.",
		"A mischievous pointer exploring the enchanted world of C++.",
		"A Java class learning about polymorphism in a whimsical realm.",
		"An epic quest of a code snippet to find the lost library.",
		"A mysterious bug in the enchanted forest of code.",
		"A journey through the infinite loops of recursion.",
		"A curious algorithm solving puzzles in Data Structure Land.",
		"The adventure of a Boolean variable searching for truth in a binary world.",
		"A playful code block that learns to scope its dreams.",
		"A function that meets an object and discovers the magic of methods.",
		"The magical debug log that reveals hidden secrets of code.",
		"A coder's adventure in the realm of machine learning.",
		"The tale of a server that dreams of becoming a microservice.",
		"The story of a data packet traveling through a network forest.",
		"A journey to the center of a binary tree.",
		"A brave little app that transforms into a full-stack hero.",
		"The secret world of code comments and the messages they hide.",
		"An epic tale of endless loops and the break that saved the day.",
		"A magical land where syntax errors turn into friendly puzzles.",
		"A coder's quest to master Git in a version control wonderland.",
		"The adventure of a compiler that speaks in riddles.",
		"A quest to find the elusive null pointer in a sea of variables.",
		"The enchanting story of a class finding its supertype.",
		"The debugging diary of a programmer battling system crashes.",
		"A whimsical journey through the realm of regular expressions.",
		"An epic battle between functions and methods in an object-oriented kingdom.",
		"A mysterious algorithm that turns chaos into order.",
		"The story of a code editor that writes its own tale.",
		"A little module that grows into a powerful framework hero.",
		"The secret life of a sandboxed process inside a virtual machine.",
		"A story of microservices that learn to communicate in harmony.",
		"A code snippet that dreams of becoming an open source library.",
		"The enchanted castle of a collaborative open source project.",
		"A developer's journey from 'Hello, World!' to a complex application.",
		"The tale of a software patch that saved an entire system.",
		"A whimsical dance of data types in the memory ballroom.",
		"An adventurous bootcamp graduate exploring the startup jungle.",
		"The mystery of the disappearing semicolon in the Code Chronicles.",
		"A journey into the depths of a stack overflow, emerging wiser.",
		"The legend of the immortal loop that finally meets a break.",
		"A playful saga of binary trees, where each node tells its own story.",
	}
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(randomPrompts), func(i, j int) {
		randomPrompts[i], randomPrompts[j] = randomPrompts[j], randomPrompts[i]
	})
	defaultPrompt := randomPrompts[0]
	randomPromptsForDisplay := randomPrompts[1:4]

	data := BookPromptData{
		DefaultPrompt: defaultPrompt,
		RandomPrompts: randomPromptsForDisplay,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := promptTmpl.Execute(w, data); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}
