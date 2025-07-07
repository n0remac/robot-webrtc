package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	. "github.com/n0remac/robot-webrtc/html"
	"github.com/sashabaranov/go-openai"
)

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

func SaveCard(card *NoteCard) error {
	cardsFileMutex.Lock()
	defer cardsFileMutex.Unlock()

	// 1) Read existing file
	var existing []NoteCard
	data, err := os.ReadFile(cardsFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", cardsFilePath, err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("unmarshal %s: %w", cardsFilePath, err)
		}
	}

	// 3) Upsert into slice
	updated := false
	for i, ec := range existing {
		if ec.ID == card.ID {
			existing[i] = *card
			updated = true
			break
		}
	}
	if !updated {
		existing = append(existing, *card)
	}

	// 4) Marshal with indentation for readability
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cards: %w", err)
	}

	// 5) Write atomically: temp + rename
	tmpPath := cardsFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, cardsFilePath); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
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
