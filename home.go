package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

var (
	selectedWords []string
	wordsMutex    sync.Mutex
	resetTimer    *time.Timer
	waitTimeout   = 5 * time.Second

	contentRegistry   = make(map[string]string)
	contentRegistryMu sync.Mutex
)

func Home(mux *http.ServeMux, websocketRegistry *CommandRegistry) {
	processContent()
	mux.HandleFunc("/", ServeNode(HomePage(websocketRegistry)))
}

func HomePage(websocketRegistry *CommandRegistry) *Node {
	id := "home"

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	websocketRegistry.RegisterWebsocket("selectedWord", func(word string, hub *Hub, data map[string]interface{}) {
		wordsMutex.Lock()
		selectedWords = append(selectedWords, word)
		wordsMutex.Unlock()

		// Stop & reset inactivity timer
		if resetTimer != nil {
			resetTimer.Stop()
		}

		resetTimer = time.AfterFunc(waitTimeout, func() {
			wordsMutex.Lock()
			defer wordsMutex.Unlock()

			contextPrompt := strings.Join(selectedWords, " ")

			// Get the ID of the current content
			currentContentID, _ := data["currentContentId"].(string)
			currentContent := ""

			// Look up the actual text from the registry
			if currentContentID != "" {
				contentRegistryMu.Lock()
				currentContent = contentRegistry[currentContentID]
				contentRegistryMu.Unlock()
			}

			allProcessedContent := loadProcessedContent("processed_content.json")
			currentContentFilename := ""
			for _, content := range allProcessedContent {
				if content.Content == currentContent {
					currentContentFilename = content.Filename
					break
				}
			}

			// Generate new content excluding the current content
			//newContent, err := selectContent(client, contextPrompt, currentContent)
			newContent, err := selectContentByKeywords(client, contextPrompt, currentContentFilename, allProcessedContent)
			if err != nil {
				log.Printf("Error selecting content: %v", err)
				return
			}

			// Build a new node with a fresh random ID
			newContentNode := NodeForContent(newContent)

			hub.Broadcast <- WebsocketMessage{
				Room:    id,
				Content: []byte(fmt.Sprintf(`{"type":"newContent","html":%q}`, newContentNode.Render())),
			}

			selectedWords = []string{}
		})
	})

	return DefaultLayout(
		Style(
			Raw(loadFile("home.css"))),
		Script(
			Raw(loadFile("home.js")),
		),
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/websocket?room="+id),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center min-h-screen",
			"data-theme": "dark",
		}),
			NavBar(),
			Div(
				Class("navigation-buttons flex justify-center space-x-4 mt-4"),
				Button(Id("back-button"), Class("btn btn-sm"), T("Back")),
				Button(Id("forward-button"), Class("btn btn-sm"), T("Forward")),
			),
			Div(
				Class("max-w-4xl mx-auto p-8 text-center space-y-4"),
				NodeForContent(`
This website is an interactive, ever-evolving platform where each piece of content can shift dynamically based on your interactions. You can click, tap, or even hover over individual words. If you hover for a couple of seconds, the word will become bold and cause the site to send a request to an AI model. If you wait a few more moments without selecting any words, the page’s text will fade out, and new content will appear.

It’s all powered by the OpenAI API, which I’m using to dynamically generate or select new text. Because this is an experimental app, sometimes the AI responses may feel inconsistent or repetitive. That’s part of the fun—I’m continually tweaking the prompts and architecture to refine the experience.

Please enjoy exploring, but keep in mind you’re using a “living” site. The words you click or hover on can guide the AI’s next output. Think of it as a conversation that unfolds in text form. Welcome, and have fun experimenting!
				`),
			),
		),
	)
}

// NavBar returns a centered navigation bar with dark theme styling.
func NavBar() *Node {
	return Nav(Class("bg-base-300 p-4 w-full"),
		Div(Class("container mx-auto flex justify-center"),
			Ul(Class("flex space-x-6"),
				Li(A(Href("/shadowreddit"), T("ShadowReddit"))),
				Li(A(Href("/story"), T("Story Generator"))),
				Li(A(Href("/video"), T("Video Conference"))),
			),
		),
	)
}

func NodeForContent(content string) *Node {
	// Generate a new random ID (UUID)
	contentID := uuid.NewString()

	// Store the content in our global registry
	contentRegistryMu.Lock()
	contentRegistry[contentID] = content
	contentRegistryMu.Unlock()

	// Build the div that references the contentID
	return Div(
		Id("content"),
		Class("centered-container"),
		Attr("data-content-id", contentID), // <-- key for the client to send back
		WrapWordsInSpans(content),
	)
}

func WrapWordsInSpans(input string) *Node {
	lines := strings.Split(input, "\n")
	var lineNodes []*Node

	for _, line := range lines {
		words := strings.Fields(line)
		var spanNodes []*Node

		for _, w := range words {
			span := Span(
				Class("selectable-word"),
				Attr("hx-ext", "ws"),
				Attr("ws-send", "click"),
				Attr("data-word", w),
				T(w),
			)

			spanNodes = append(spanNodes, span)
		}

		lineNode := P(
			Ch(spanNodes),
		)
		lineNodes = append(lineNodes, lineNode)
	}

	return Ch(lineNodes)
}

// ContentSelectionResponse represents the structure of the function call response.
type ContentSelectionResponse struct {
	Content string `json:"content"`
}

func loadFile(filename string) string {
	jsContent, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Error reading file: %s: %v", filename, err)
	}
	return string(jsContent)
}

func loadProcessedContent(filename string) []ContentWithKeywords {
	data, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Error reading processed content file: %v", err)
	}
	var contentList []ContentWithKeywords
	if err := json.Unmarshal(data, &contentList); err != nil {
		log.Fatalf("Error unmarshaling processed content file: %v", err)
	}
	return contentList
}

type KeywordsSelectionResponse struct {
	Filename string `json:"filename"`
}

func selectContentByKeywords(client *openai.Client, contextPrompt string, currentFilename string, processed []ContentWithKeywords) (string, error) {
	// Remove the current content from the allowed candidates.
	var candidates []ContentWithKeywords
	for _, cw := range processed {
		if strings.TrimSpace(cw.Filename) != strings.TrimSpace(currentFilename) {
			candidates = append(candidates, cw)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no allowed content available after excluding current content")
	}

	// Build a candidate list string of the form:
	// filename1: keyword1, keyword2, …
	// filename2: keywordA, keywordB, …
	var candidateLines []string
	for _, candidate := range candidates {
		keywordsStr := strings.Join(candidate.Keywords, ", ")
		line := fmt.Sprintf("%s: %s", candidate.Filename, keywordsStr)
		candidateLines = append(candidateLines, line)
	}
	candidatesStr := strings.Join(candidateLines, "\n")

	// Construct the user prompt.
	userMsgStr := fmt.Sprintf(`Based on the context words, choose a filename from the following list that best matches the context.

Candidates:
%s`, candidatesStr)

	// Define the system prompt.
	systemPrompt := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are an assistant that selects content based on keywords. Given the context and a candidate list with filenames and their associated keywords, return only the filename of the content that best fits the provided context.",
	}

	// Build the user message that includes the context and candidate list.
	userMessage := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: fmt.Sprintf("Context: %s\n\n%s", contextPrompt, userMsgStr),
	}

	// Define the function for selecting content by keywords.
	fn := openai.FunctionDefinition{
		Name:        "select_content_by_keywords",
		Description: "Select a filename from a list of candidates based on keywords and context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"filename"},
		},
	}

	// Build the chat completion request.
	chatRequest := openai.ChatCompletionRequest{
		Model: "gpt-4-0613",
		Messages: []openai.ChatCompletionMessage{
			systemPrompt,
			userMessage,
		},
		Functions:    []openai.FunctionDefinition{fn},
		FunctionCall: openai.FunctionCall{Name: "select_content_by_keywords"},
	}

	chatResp, err := client.CreateChatCompletion(context.Background(), chatRequest)
	if err != nil {
		return "", fmt.Errorf("failed to get response from OpenAI: %w", err)
	}

	choice := chatResp.Choices[0]
	if choice.Message.FunctionCall == nil {
		return "", fmt.Errorf("no function call in OpenAI response")
	}

	// Parse the function call arguments.
	var parsed KeywordsSelectionResponse
	err = json.Unmarshal([]byte(choice.Message.FunctionCall.Arguments), &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal function response: %w", err)
	}

	// Search through the processed content slice for the filename.
	for _, candidate := range processed {
		if candidate.Filename == parsed.Filename {
			return candidate.Content, nil
		}
	}

	return "", fmt.Errorf("filename %s not found among candidates", parsed.Filename)
}
