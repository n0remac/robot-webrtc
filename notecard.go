package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type NoteCard struct {
	ID          string
	Entry       string
	AIEntry     string
	ImagePrompt string
	ImageURL    string
	Done        bool
	Error       error
}

var (
	cardSessions      = make(map[string]*NoteCard)
	cardSessionsMutex sync.Mutex
)

func Notecard(mux *http.ServeMux, registry *CommandRegistry) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	registry.RegisterWebsocket("createNotecard", func(_ string, hub *Hub, data map[string]interface{}) {
		entry := data["entry"].(string)

		card := &NoteCard{
			ID:    "c" + uuid.NewString(),
			Entry: entry,
			Done:  false,
			Error: nil,
		}
		cardSessionsMutex.Lock()
		cardSessions[card.ID] = card
		cardSessionsMutex.Unlock()

		go func(card *NoteCard, hub *Hub) {
			description, imagePrompt, err := generateCardContent(client, card.Entry)
			if err != nil {
				card.Error = err
				card.Done = true
				return
			}
			card.AIEntry = description
			card.ImagePrompt = imagePrompt
			card.Done = true

			hub.Broadcast <- WebsocketMessage{
				Room:    "notecard",
				Content: []byte(createNoteCardDiv(card).Render()),
			}

			fmt.Println("Generating image for card:", card.ID)
			img, err := generateCardImage(client, card, "notecards", "notecards")
			if err != nil {
				card.Error = err
				card.Done = true
				return
			}
			card.ImageURL = img
			fmt.Println("Image generated for card:", card.ID)

			hub.Broadcast <- WebsocketMessage{
				Room:    "notecard",
				Content: []byte(createNoteCardDiv(card).Render()),
			}
		}(card, hub)

		content := Div(
			Id("notes"),
			Attr("hx-swap-oob", "beforeend"),
			Div(
				Attr("hx-swap-oob", "outerHTML"),
				createNoteCardDiv(card),
			),
		)

		hub.Broadcast <- WebsocketMessage{
			Room:    "notecard",
			Content: []byte(content.Render()),
		}
	})

	mux.HandleFunc("/notecard", serveCardThreadPage)
	mux.Handle("/notecards/", http.StripPrefix("/notecards/", http.FileServer(http.Dir("notecards"))))

	mux.HandleFunc("/ws/createNotecard", createWebsocket(registry))
}

func serveCardThreadPage(w http.ResponseWriter, r *http.Request) {
	page := DefaultLayout(
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/ws/createNotecard?room=notecard"),
		Div(
			Id("main-content"),
			Class("container mx-auto p-4"),
			Div(
				Id("notes"),
				Attr("hx-swap-oob", "beforeend"),
				Class("space-y-4 flex flex-col"),
			),
			Form(
				Attr("ws-send", "submit"),
				Input(
					Type("hidden"),
					Name("type"),
					Value("createNotecard"),
				),
				TextArea(
					Name("entry"),
					Rows(4),
					Placeholder("Enter your note here..."),
				),
				Input(
					Type("submit"),
					Class("btn btn-primary w-32"),
					Value("Post"),
				),
			),
		),
	)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func generateCardContent(client *openai.Client, prompt string) (string, string, error) {
	system := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are generating a trading card. You will extract a short description and a vivid image prompt from the user's entry. Use the users voice or quote to create a concise entry for the card. The entry should be less then 25 words. The image prompt should be detailed and suitable for generating an image.",
	}
	user := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: fmt.Sprintf("Note: %s", prompt),
	}
	fn := openai.FunctionDefinition{
		Name: "make_card",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entry":        map[string]string{"type": "string"},
				"image_prompt": map[string]string{"type": "string"},
			},
			"required": []string{"entry", "image_prompt"},
		},
	}

	req := openai.ChatCompletionRequest{
		Model:        "gpt-4-0613",
		Messages:     []openai.ChatCompletionMessage{system, user},
		Functions:    []openai.FunctionDefinition{fn},
		FunctionCall: openai.FunctionCall{Name: "make_card"},
	}
	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		Entry       string `json:"entry"`
		ImagePrompt string `json:"image_prompt"`
	}
	err = json.Unmarshal([]byte(resp.Choices[0].Message.FunctionCall.Arguments), &parsed)
	return parsed.Entry, parsed.ImagePrompt, err
}

func createNoteCardDiv(c *NoteCard) *Node {
	bgStyle := ""
	if c.ImageURL != "" {
		bgStyle = fmt.Sprintf(
			"background-image:url('%s');background-size:cover;background-position:center;",
			c.ImageURL,
		)
	}
	entry := c.Entry
	if c.AIEntry != "" {
		entry = c.AIEntry
	}

	return Div(
		Attr("hx-swap-oob", "outerHTML"),
		Id(c.ID),
		Class("box-border w-[240] aspect-[2.5/3.5] border-2 rounded-lg shadow-md overflow-hidden relative"),
		Attr("style", bgStyle),

		// inset overlay with padding on both sides
		Div(
			Class("absolute inset-x-2 bottom-2 bg-neutral-400 opacity-75 p-2 rounded-xl"),
			Div(
				Class("text-black text-sm whitespace-normal break-words hyphens-none text-center font-bold"),
				T(entry),
			),
		),
	)
}

func createSheetDiv(cards []*NoteCard) *Node {
	// Pad to full sheets of 8
	for len(cards) < 8 {
		cards = append(cards, &NoteCard{})
	}
	panels := make([]*Node, 0, 8)
	for _, c := range cards {
		panel := Div(
			Class("flex justify-center items-center w-full h-full"),
			// Only render a card if it has content
			Ch(func() []*Node {
				if c.ID == "" {
					// empty slot
					return nil
				}
				return []*Node{createNoteCardDiv(c)}
			}()),
		)
		panels = append(panels, panel)
	}
	// Wrap panels in the .print-sheet grid
	return Div(
		Class("print-sheet grid grid-cols-4 grid-rows-2 gap-0 border border-gray-300 mb-4"),
		Ch(panels),
	)
}

func generateCardImage(client *openai.Client, card *NoteCard, assetDir, urlPrefix string) (string, error) {
	// 1) Request image from OpenAI
	fmt.Println("Generating image with prompt:", card.ImagePrompt)
	imgResp, err := client.CreateImage(context.Background(), openai.ImageRequest{
		Prompt: fmt.Sprintf("Illustration based on the following description: %s. No text in the image.", card.ImagePrompt),
		N:      1,
		Size:   "512x512",
	})
	if err != nil {
		return "", fmt.Errorf("image generation error for card %s: %w", card.ID, err)
	}
	if len(imgResp.Data) == 0 {
		return "", fmt.Errorf("no image data returned for card %s", card.ID)
	}

	// 2) Download the generated image
	imageURL := imgResp.Data[0].URL
	res, err := http.Get(imageURL)
	if err != nil {
		return "", fmt.Errorf("error downloading image for card %s: %w", card.ID, err)
	}
	defer res.Body.Close()

	imgBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("error reading image data for card %s: %w", card.ID, err)
	}

	// 3) Ensure asset directory exists
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		return "", fmt.Errorf("error creating asset directory %s: %w", assetDir, err)
	}

	// 4) Save the image file: <cardID>.png
	filename := fmt.Sprintf("%s.png", card.ID)
	fullPath := filepath.Join(assetDir, filename)
	if err := os.WriteFile(fullPath, imgBytes, 0644); err != nil {
		return "", fmt.Errorf("error writing image file for card %s: %w", card.ID, err)
	}

	// 5) Return the public URL path prefix + filename
	// e.g. urlPrefix="/static/cards/" -> "/static/cards/<cardID>.png"
	return filepath.ToSlash(filepath.Join(urlPrefix, filename)), nil
}
