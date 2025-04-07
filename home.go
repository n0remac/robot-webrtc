package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	waitTimeout   = 3 * time.Second

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
				Content: []byte(newContentNode.Render()),
			}

			selectedWords = []string{}
		})
	})

	return DefaultLayout(
		Style(
			Raw(`
				.selectable-word {
					margin: 0 2px;
				}
			`)),
		Script(
			Raw(`
				document.addEventListener('htmx:wsConfigSend', function(evt) {
					const clickedElt = evt.detail.elt;
					const word = clickedElt.getAttribute('data-word');
					if (word) {
						evt.detail.parameters.selectedWord = word;
					}

					const currentContentElt = document.getElementById('content');
					if (currentContentElt) {
						const contentId = currentContentElt.getAttribute('data-content-id');
						evt.detail.parameters.currentContentId = contentId;
					}
				});
			`)),
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/websocket?room="+id),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center min-h-screen",
			"data-theme": "dark",
		}),
			NavBar(),
			Div(
				Class("max-w-4xl mx-auto p-8 text-center space-y-4"),
				H1(Class("text-3xl font-bold"),
					T("Welcome to My Portfolio Site")),
				NodeForContent(
					"Welcome to my interactive portfolio—a dynamic showcase where innovation meets creativity. "+
						"Explore a curated collection of my personal projects, such as ShadowReddit, an immersive simulation of online community debates, "+
						"and a Children's Book Generator that transforms simple ideas into imaginative stories. "+
						"Engage with the content by clicking on individual words to trigger real-time, AI-driven narrative updates. "+
						"Discover how technology and creativity combine to bring each project to life. Feel free to explore the links above to dive deeper into each app!",
				),
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

func WrapWordsInSpans(input string) *Node {
	words := strings.Fields(input)
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

	container := Div(
		Class("wrapped-text flex flex-wrap gap-1"),
		Ch(spanNodes),
	)
	return container
}

var AllContent = []string{
	"Exploring Minimalism: In this blog post, I share insights on simplifying life and finding joy in less. Embrace minimalism and discover what truly matters.",
	"Tech Trends 2025: The future of technology is now. This post dives into emerging trends—from AI innovations to sustainable tech solutions.",
	"Travel Tales: Journey through hidden gems across the globe. Learn how adventure transforms life and sparks creativity.",
	"Healthy Living: Discover practical tips for a balanced lifestyle. Nutrition, exercise, and mindfulness come together for a healthier you.",
	"Creative Writing: Unleash your inner storyteller. This post offers inspiration and techniques to kickstart your writing journey.",
}

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
		Attr("data-content-id", contentID), // <-- key for the client to send back
		WrapWordsInSpans(content),
	)
}
