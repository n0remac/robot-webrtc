package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

			// Generate new content excluding the current content
			newContent, err := selectContent(client, contextPrompt, currentContent)
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

var AllContent = loadAllContent("content")

// ContentSelectionResponse represents the structure of the function call response.
type ContentSelectionResponse struct {
	Content string `json:"content"`
}

func selectContent(client *openai.Client, contextPrompt string, currentContent string) (string, error) {
	// Filter out the current content from the allowed content.
	var allowedContent []string
	for _, content := range AllContent {
		if strings.TrimSpace(content) != strings.TrimSpace(currentContent) {
			allowedContent = append(allowedContent, content)
		}
	}
	if len(allowedContent) == 0 {
		return "", fmt.Errorf("no allowed content available after excluding current content")
	}

	// Marshal the allowedContent to JSON so it can be passed to GPT.
	allowedContentJSON, err := json.Marshal(allowedContent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal allowed content: %w", err)
	}

	// Define the system prompt.
	systemPrompt := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are helping to select a single piece of content from a list of predefined options. Choose the one that best fits the context provided. Use only the content provided in the list; do not invent new content.",
	}

	// Build the user message with the context and full list (excluding the current content).
	userMessage := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		Content: fmt.Sprintf(`Context: %s

Here is the full list of allowed content (excluding the current content):
%s`, contextPrompt, string(allowedContentJSON)),
	}

	// Define the function for selecting a single piece of content.
	fn := openai.FunctionDefinition{
		Name:        "select_content",
		Description: "Select a single piece of content from a list of predefined options.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"content"},
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
		FunctionCall: openai.FunctionCall{Name: "select_content"},
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
	var parsed ContentSelectionResponse
	err = json.Unmarshal([]byte(choice.Message.FunctionCall.Arguments), &parsed)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal function response: %w", err)
	}

	return parsed.Content, nil
}

func loadFile(filename string) string {
	jsContent, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalf("Error reading file: %s: %v", filename, err)
	}
	return string(jsContent)
}

func loadAllContent(dirPath string) []string {
	var contentList []string

	// Read the directory entries
	files, err := os.ReadDir(dirPath)
	if err != nil {
		log.Fatalf("Error reading content directory: %v", err)
	}

	for _, file := range files {
		if file.IsDir() {
			// Skip directories, or recursively scan if desired
			continue
		}
		// Construct full path
		fullPath := filepath.Join(dirPath, file.Name())

		data, err := os.ReadFile(fullPath)
		if err != nil {
			log.Printf("Warning: failed to read file %s: %v", fullPath, err)
			continue
		}
		// Convert the file’s data to a string, trim, and add to the slice
		contentStr := strings.TrimSpace(string(data))
		if len(contentStr) > 0 {
			contentList = append(contentList, contentStr)
		}
	}
	return contentList
}
