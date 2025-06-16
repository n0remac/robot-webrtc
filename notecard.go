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
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
)

type Voters struct {
	Voters []string `json:"voters"`
}

type NoteCard struct {
	ID          string
	RoomID      string
	Entry       string
	AIEntry     string
	ImagePrompt string
	ImageURL    string
	UpVotes     []string
	DownVotes   []string
}

var (
	cardSessions      = make(map[string]*NoteCard)
	cardSessionsMutex sync.Mutex
	cardsFilePath     = "notecards/cards.json"
	cardsFileMutex    = &sync.Mutex{}
)

func Notecard(mux *http.ServeMux, registry *CommandRegistry) {
	// store a user id in local storage

	registerVoting(mux, registry)
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	registry.RegisterWebsocket("createNotecard", func(_ string, hub *Hub, data map[string]interface{}) {
		entry := data["entry"].(string)
		roomID := data["roomID"].(string)

		card := &NoteCard{
			ID:        "c" + uuid.NewString(),
			Entry:     entry,
			RoomID:    roomID,
			UpVotes:   []string{},
			DownVotes: []string{},
		}
		cardSessionsMutex.Lock()
		cardSessions[card.ID] = card
		cardSessionsMutex.Unlock()

		go func(card *NoteCard, hub *Hub) {
			description, imagePrompt, err := generateCardContent(client, card.Entry)
			if err != nil {
				return
			}
			card.AIEntry = description
			card.ImagePrompt = imagePrompt

			hub.Broadcast <- WebsocketMessage{
				Room:    roomID,
				Content: []byte(createEditableCardDiv(card).Render()),
			}

			fmt.Println("Generating image for card:", card.ID)
			img, err := generateCardImage(client, card, "notecards", "/notecards")
			if err != nil {
				return
			}
			card.ImageURL = img
			fmt.Println("Image generated for card:", card.ID)

			if err := SaveCard(card); err != nil {
				log.Printf("Error saving card %s: %v", card.ID, err)
				return
			}

			hub.Broadcast <- WebsocketMessage{
				Room:    roomID,
				Content: []byte(createEditableCardDiv(card).Render()),
			}
		}(card, hub)

		content := Div(
			Id("notes"),
			Attr("hx-swap-oob", "afterbegin"),
			Div(
				createEditableCardDiv(card),
			),
		)

		hub.Broadcast <- WebsocketMessage{
			Room:    roomID,
			Content: []byte(content.Render()),
		}
	})
	registry.RegisterWebsocket("notecardCreatingTab", func(_ string, hub *Hub, data map[string]interface{}) {
		roomId := data["roomId"].(string)

		hub.Broadcast <- WebsocketMessage{
			Room:    roomId,
			Content: []byte(createNoteCardPage(roomId).Render()),
		}
	})

	mux.HandleFunc("/notecard/{id...}", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			serveCardThreadPage(w, r)

		case http.MethodPut:
			// show edit form
			cardId := r.FormValue("cardId")
			cards, err := loadCards()
			if err != nil {
				http.Error(w, "could not load cards", http.StatusInternalServerError)
				return
			}
			card, err := getCard(cardId, cards)
			if err != nil {
				http.Error(w, fmt.Sprintf("Card not found: %v", err), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(createEditNoteCardDiv(card).Render()))

		case http.MethodPatch:
			// handle update
			// 1) parse form (allows file upload)
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				http.Error(w, "invalid form data", http.StatusBadRequest)
				return
			}

			// 2) lookup the card
			cardId := r.FormValue("cardId")
			cards, err := loadCards()
			if err != nil {
				http.Error(w, "could not load cards", http.StatusInternalServerError)
				return
			}
			card, err := getCard(cardId, cards)
			if err != nil {
				http.Error(w, fmt.Sprintf("Card not found: %v", err), http.StatusNotFound)
				return
			}

			// 3) update entry
			if entry := r.FormValue("entry"); entry != "" {
				card.Entry = entry
			}

			// 4) process image upload (if any)
			file, fh, err := r.FormFile("image")
			if err == nil {
				defer file.Close()
				// ensure notecards dir exists
				if err := os.MkdirAll("notecards", 0755); err != nil {
					http.Error(w, "could not save image", http.StatusInternalServerError)
					return
				}
				// pick an extension
				ext := filepath.Ext(fh.Filename)
				if ext == "" {
					ext = ".png"
				}
				filename := card.ID + ext
				dst := filepath.Join("notecards", filename)
				out, err := os.Create(dst)
				if err != nil {
					http.Error(w, "could not save image", http.StatusInternalServerError)
					return
				}
				defer out.Close()
				if _, err := io.Copy(out, file); err != nil {
					http.Error(w, "could not write image", http.StatusInternalServerError)
					return
				}
				// update the public URL
				card.ImageURL = "/notecards/" + filename
			}
			// if err != nil, no file was uploaded—leave ImageURL unchanged

			// 5) persist the change
			if err := SaveCard(card); err != nil {
				http.Error(w, "could not save card", http.StatusInternalServerError)
				return
			}

			// 6) re-render the detail page with updated data
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(createEditableCardDiv(card).Render()))

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.Handle("/notecards/", http.StripPrefix("/notecards/", http.FileServer(http.Dir("notecards"))))
	mux.HandleFunc("/ws/createNotecard", createWebsocket(registry))
}

func serveCardThreadPage(w http.ResponseWriter, r *http.Request) {
	roomId := r.PathValue("id")
	if roomId == "" {
		roomId = "r" + uuid.NewString()
		http.Redirect(w, r, fmt.Sprintf("/notecard/%s", roomId), http.StatusFound)
	}
	// open the cards file to read existing cards
	cardsFileMutex.Lock()
	defer cardsFileMutex.Unlock()
	if _, err := os.Stat(cardsFilePath); os.IsNotExist(err) {
		// If the file doesn't exist, create an empty one
		if err := os.WriteFile(cardsFilePath, []byte("[]"), 0644); err != nil {
			http.Error(w, fmt.Sprintf("Error creating cards file: %v", err), http.StatusInternalServerError)
			return
		}
	}

	page := DefaultLayout(
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/ws/createNotecard?room="+roomId),
		Attr("data-theme", "dark"),
		Style(Raw(`
			.fade-in.htmx-added {
				opacity: 0;
			}
			.fade-in {
				opacity: 1;
				transition: opacity 1s ease-out;
			}
		`)),
		Div(
			Class("navbar bg-base-100 shadow-sm justify-center"),
			Form(
				Class("space-y-4 m-4"),
				Attr("ws-send", "submit"),
				Input(
					Type("hidden"),
					Name("type"),
					Value("notecardCreatingTab"),
				),
				Input(
					Type("hidden"),
					Name("roomId"),
					Value(roomId),
				),
				Input(
					Type("submit"),
					Class("btn btn-ghost text-xl"),
					Value("Create"),
				),
			),
			Form(
				Class("space-y-4 m-4"),
				Attr("ws-send", "submit"),
				Input(
					Type("hidden"),
					Name("type"),
					Value("notecardVoting"),
				),
				Input(
					Type("hidden"),
					Name("roomId"),
					Value(roomId),
				),
				Input(
					Type("submit"),
					Class("btn btn-ghost text-xl"),
					Value("Vote"),
				),
			),
			Form(
				Class("space-y-4 m-4"),
				Attr("ws-send", "submit"),
				Input(
					Type("hidden"),
					Name("type"),
					Value("notecardRanking"),
				),
				Input(
					Type("hidden"),
					Name("roomId"),
					Value(roomId),
				),
				Input(
					Type("submit"),
					Class("btn btn-ghost text-xl"),
					Value("Ranking"),
				),
			),
		),
		createNoteCardPage(roomId),
	)

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func createNoteCardPage(roomId string) *Node {
	// Read existing cards
	cards, err := loadCards()
	if err != nil {
		fmt.Println("No cards available, initializing empty slice")
		cards = []NoteCard{}
	}

	// Filter cards for the current room
	var cardsInRoom []*NoteCard
	for _, card := range cards {
		if card.RoomID == roomId {
			cardsInRoom = append(cardsInRoom, &card)
		}
	}
	slices.Reverse(cardsInRoom)
	// Create the page with existing cards
	cardDivs := make([]*Node, 0, len(cardsInRoom))
	for _, card := range cardsInRoom {

		createNode := createEditableCardDiv(card)

		cardDivs = append(cardDivs, Div(
			// TODO: click to page goes here
			Name(card.ID),
			createNode),
		)
	}

	return Div(
		Id("main-content"),
		Class("container mx-auto p-4"),
		H1(
			Class("text-2xl font-bold mb-4 flex justify-center"),
			T("Note to Card Converter"),
		),
		P(
			Class("mb-4 flex justify-center"),
			T("Enter a note below and it will be converted into a trading card with an AI-generated image. You can then vote on the cards created by others or view the rankings."),
		),
		Form(
			Class("space-y-4 m-4"),
			Attr("ws-send", "submit"),
			Input(
				Type("hidden"),
				Name("type"),
				Value("createNotecard"),
			),
			Input(
				Type("hidden"),
				Name("roomID"),
				Value(roomId),
			),
			TextArea(
				Class("textarea textarea-bordered w-full h-32"),
				Name("entry"),
				Rows(4),
				Placeholder("Enter your note here..."),
			),
			Div(Input(
				Type("submit"),
				Class("btn btn-primary w-32"),
				Value("Post"),
			)),
		),
		H2(
			Class("text-xl font-semibold mb-4 flex justify-center"),
			T("Your Cards"),
		),
		Div(
			Id("notes"),
			Attr("hx-swap", "beforeend settle:1s"),
			Class("space-y-4 flex flex-col items-center"),
			Ch(cardDivs),
		),
	)
}

func createEditableCardDiv(card *NoteCard) *Node {
	createNode := createNoteCardDiv(card)
	createNode.Children = append(createNode.Children, Span(
		Attr("hx-trigger", "click"),
		Attr("hx-put", "/notecard/?cardId="+card.ID),
		Attr("hx-target", "#"+card.ID),
		Attr("hx-swap", "outerHTML"),
		Class("text-black text-2xl font-bold absolute z-10 bg-neutral-400 opacity-75 p-1 m-2 rounded-full right-0"),
		T("🖉"),
	))
	return createNode
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
func createEditNoteCardDiv(c *NoteCard) *Node {
	bgStyle := ""
	if c.ImageURL != "" {
		bgStyle = fmt.Sprintf(
			"background-image:url('%s');background-size:cover;background-position:center;",
			c.ImageURL,
		)
	}
	entry := c.Entry
	words := strings.Split(entry, " ")
	if len(words) > 35 {
		entry = c.AIEntry
	}
	editURL := fmt.Sprintf("/notecard/?cardId=%s", c.ID)

	return Div(
		Id(c.ID),
		Class("fade-in box-border w-[240] aspect-[2.5/3.5] border-2 rounded-lg shadow-md overflow-hidden relative"),
		Attr("style", bgStyle),

		Form(
			// HTMX will send a PUT request with multipart/form-data
			Attr("hx-patch", editURL),
			Attr("hx-encoding", "multipart/form-data"),
			Attr("hx-target", "#"+c.ID),
			Attr("hx-swap", "outerHTML"),

			// hidden payload
			Input(Type("hidden"), Name("cardId"), Value(c.ID)),
			Div(
				Class("bg-neutral-50 rounded-lg"),
				Label(
					Class("block font-semibold bg-gray-950 text-center"),
					T("Update image"),
				),
				Input(
					Type("file"),
					Name("image"),
					Class("file-input file-input-bordered w-full"),
				),
			),
			Div(
				Class("absolute inset-x-2 bottom-2 bg-neutral-400 opacity-75 p-2 rounded-xl"),
				Div(
					Class("text-black text-sm whitespace-normal break-words hyphens-none text-center font-bold"),
					TextArea(
						Class("textarea textarea-bordered w-full bg-neutral-50"),
						Name("entry"),
						T(entry),
					),
				),
			),

			// Submit button
			Div(
				Class("flex justify-end"),
				Input(
					Type("submit"),
					Class("btn btn-primary rounded-full bottom-2 right-2 absolute"),
					Value("Save"),
				),
			),
		),
	)
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
	words := strings.Split(entry, " ")
	if len(words) > 35 {
		entry = c.AIEntry
	}

	return Div(
		Id(c.ID),
		Class("fade-in box-border w-[240] aspect-[2.5/3.5] border-2 rounded-lg shadow-md overflow-hidden relative"),
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

func CardDetailPage(card NoteCard) *Node {
	return Div(
		Id("main-content"),
		Class("container mx-auto p-6 space-y-6"),

		// Title
		H1(Class("text-3xl font-bold"), T("Card Details")),

		// Show the raw fields
		Div(
			Class("space-y-2"),
			// ID
			Div(
				Class("flex"),
				Span(Class("font-semibold w-32"), T("ID:")),
				Span(T(card.ID)),
			),
			// RoomID
			Div(
				Class("flex"),
				Span(Class("font-semibold w-32"), T("Room ID:")),
				Span(T(card.RoomID)),
			),
			// Entry
			Div(
				Class("flex"),
				Span(Class("font-semibold w-32"), T("Entry:")),
				Span(T(card.Entry)),
			),
			// AIEntry
			Div(
				Class("flex"),
				Span(Class("font-semibold w-32"), T("AI Entry:")),
				Span(T(card.AIEntry)),
			),
			// ImagePrompt
			Div(
				Class("flex"),
				Span(Class("font-semibold w-32"), T("Image Prompt:")),
				Span(T(card.ImagePrompt)),
			),
			// ImageURL + preview
			Div(
				Class("flex flex-col"),
				Span(Class("font-semibold"), T("Image:")),
				Img(
					Attr("src", card.ImageURL),
					Attr("alt", "Card image"),
					Class("mt-2 w-48 h-48 object-cover rounded"),
				),
			),
			// UpVotes list
			Div(
				Class("flex flex-col"),
				Span(Class("font-semibold"), T("Up Votes:")),
				Div(
					Class("flex flex-wrap gap-2 mt-1"),
					Ch(func() []*Node {
						var badges []*Node
						for _, uid := range card.UpVotes {
							badges = append(badges,
								Span(
									Class("px-2 py-1 bg-green-200 text-green-800 rounded"),
									T(uid),
								),
							)
						}
						if len(badges) == 0 {
							badges = []*Node{Span(T("None"))}
						}
						return badges
					}()),
				),
			),
			// DownVotes list
			Div(
				Class("flex flex-col"),
				Span(Class("font-semibold"), T("Down Votes:")),
				Div(
					Class("flex flex-wrap gap-2 mt-1"),
					Ch(func() []*Node {
						var badges []*Node
						for _, uid := range card.DownVotes {
							badges = append(badges,
								Span(
									Class("px-2 py-1 bg-red-200 text-red-800 rounded"),
									T(uid),
								),
							)
						}
						if len(badges) == 0 {
							badges = []*Node{Span(T("None"))}
						}
						return badges
					}()),
				),
			),
		),
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
