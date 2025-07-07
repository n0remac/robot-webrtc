package cards

import (
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
	. "github.com/n0remac/robot-webrtc/html"
	. "github.com/n0remac/robot-webrtc/websocket"
	"github.com/sashabaranov/go-openai"
)

type Voters struct {
	Voters []string `json:"voters"`
}

type NoteCard struct {
	ID         string
	RoomID     string
	ShortEntry string
	LongEntry  string
	ImageURL   string
	// TODO think about moving these to a separate struct

	AIEntry     string
	ImagePrompt string
	UpVotes     []string
	DownVotes   []string
}

type AppConfig struct {
	DB string `json:"db"`
}

var (
	cardSessions      = make(map[string]*NoteCard)
	cardSessionsMutex sync.Mutex
	cardsFilePath     = "notecards/cards.json"
	cardsFileMutex    = &sync.Mutex{}
)

func Notecard(mux *http.ServeMux, registry *CommandRegistry) {
	// docs := db.NewSqliteDocumentStore("data/docs.db")
	// deps := &deps.Deps{
	// 	DB:   db.LoadDB("sqlite://data/db.sqlite"),
	// 	Docs: docs,
	// }

	registerVoting(mux, registry)
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	mux.HandleFunc("/framedcard/{id...}", func(w http.ResponseWriter, r *http.Request) {
		var card *NoteCard
		switch r.Method {
		case http.MethodGet:
			cardId := r.FormValue("id")
			roomId := r.FormValue("roomID")
			fmt.Println("ROOM: ", roomId)
			shortEntry := r.FormValue("ShortEntry")
			longEntry := r.FormValue("LongEntry")

			if cardId == "" {
				cardId = "c" + uuid.NewString()
				card = &NoteCard{
					ID:         cardId,
					ShortEntry: shortEntry,
					LongEntry:  longEntry,
					ImageURL:   "",
					RoomID:     roomId,
					UpVotes:    []string{},
					DownVotes:  []string{},
				}
			} else {
				cards, err := loadCards()
				if err != nil {
					http.Error(w, "could not load cards", http.StatusInternalServerError)
					return
				}
				card, err = getCard(cardId, cards)
				card.ShortEntry = shortEntry
				card.LongEntry = longEntry

				if err != nil {
					http.Error(w, fmt.Sprintf("Card not found: %v", err), http.StatusNotFound)
					return
				}
			}

			err := SaveCard(card)
			if err != nil {
				fmt.Println("Error: ", err)
			}

			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(FramedCard(card, createNoteCardDiv(card)).Render()))
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
			roomId := r.FormValue("roomID")
			cards, err := loadCards()
			if err != nil {
				http.Error(w, "could not load cards", http.StatusInternalServerError)
				return
			}
			card, err := getCard(cardId, cards)
			if err != nil {
				card = &NoteCard{
					ID:         "c" + uuid.NewString(),
					ShortEntry: "",
					LongEntry:  "",
					ImageURL:   "",
					RoomID:     roomId,
					UpVotes:    []string{},
					DownVotes:  []string{},
				}
			}

			// 3) update entry
			if shortEntry := r.FormValue("ShortEntry"); shortEntry != "" {
				fmt.Println("Updating short entry for card:", card.ID)
				card.ShortEntry = shortEntry
			}

			if longEntry := r.FormValue("LongEntry"); longEntry != "" {
				fmt.Println("Updating long entry for card:", card.ID)
				card.LongEntry = longEntry
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
			w.Write([]byte(createNoteCardDiv(card).Render()))

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.Handle("/notecards/", http.StripPrefix("/notecards/", http.FileServer(http.Dir("notecards"))))
	mux.HandleFunc("/ws/createNotecard", CreateWebsocket(registry))

	registry.RegisterWebsocket("createNotecard", func(_ string, hub *Hub, data map[string]interface{}) {
		entry := data["entry"].(string)
		roomID := data["roomID"].(string)

		card := &NoteCard{
			ID:         "c" + uuid.NewString(),
			ShortEntry: entry,
			RoomID:     roomID,
			UpVotes:    []string{},
			DownVotes:  []string{},
		}
		cardSessionsMutex.Lock()
		cardSessions[card.ID] = card
		cardSessionsMutex.Unlock()

		go func(card *NoteCard, hub *Hub) {
			description, imagePrompt, err := generateCardContent(client, card.ShortEntry)
			if err != nil {
				return
			}
			card.AIEntry = description
			card.ImagePrompt = imagePrompt

			WsHub.Broadcast <- WebsocketMessage{
				Room:    roomID,
				Content: []byte(FramedCard(card, createNoteCardDiv(card)).Render()),
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

			WsHub.Broadcast <- WebsocketMessage{
				Room:    roomID,
				Content: []byte(FramedCard(card, createNoteCardDiv(card)).Render()),
			}
		}(card, hub)

		content := Div(
			Id("notes"),
			Attr("hx-swap-oob", "afterbegin"),
			Div(
				FramedCard(card, createNoteCardDiv(card)),
			),
		)

		WsHub.Broadcast <- WebsocketMessage{
			Room:    roomID,
			Content: []byte(content.Render()),
		}
	})
	registry.RegisterWebsocket("notecardCreatingTab", func(_ string, hub *Hub, data map[string]interface{}) {
		roomId := data["roomId"].(string)

		WsHub.Broadcast <- WebsocketMessage{
			Room:    roomId,
			Content: []byte(createNoteCardPage(roomId).Render()),
		}
	})
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
		cardDivs = append(cardDivs, FramedCard(card, createNoteCardDiv(card)))
	}

	emptyCard := &NoteCard{
		RoomID: roomId,
	}

	return Div(
		Id("main-content"),
		Class("container mx-auto p-4 flex flex-col items-center"),
		H1(
			Class("text-3xl font-bold mb-4 text-center"),
			T("Card Creation"),
		),
		createEditNoteCardDiv(emptyCard),
		H2(
			Class("text-xl font-semibold m-4 flex justify-center"),
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

func FramedCard(card *NoteCard, cardNode *Node) *Node {
	editHidden := ""
	saveHidden := "hidden"
	if card.ID == "" {
		editHidden = "hidden"
		saveHidden = ""
	}

	frame := Div(
		// outer “frame” border wrapping the card
		Class("inline-block border-4 border-zinc-500 bg-zinc-500 shadow-md pl-2 pr-2"),

		Div(
			Class("flex justify-between mb-2"),

			Div(
				Id(card.ID+"-frame-flip"),
				Input(

					Id("flip-btn-"+card.ID),
					Type("submit"),
					Class("btn btn-ghost bg-gray-600 rounded-none"),
					Value("Flip"),
				),
			),
		),

		// The card itself gets swapped in/out by its own id
		cardNode,

		// Footer bar for the Page and Edit buttons
		Div(
			Class("flex justify-between mt-2"),

			// “Edit” button on the right
			Form(
				Attr("hx-put", fmt.Sprintf("/notecard/?cardId=%s", card.ID)),
				Attr("hx-target", "#"+card.ID),
				Attr("hx-swap", "outerHTML"),

				Input(Type("hidden"), Name("cardId"), Value(card.ID)),

				Input(
					Id(card.ID+"-frame-edit"),
					Type("submit"),
					Script(Raw(fmt.Sprintf(`( () => {document.getElementById('%s-frame-edit').onclick = function () {
						document.getElementById('%s-frame-save').classList.toggle("hidden"); 
						document.getElementById('%s-frame-edit').classList.toggle("hidden");
					}})()`, card.ID, card.ID, card.ID))),
					Class("btn btn-ghost bg-gray-600 rounded-none "+editHidden),
					Value("Edit"),
				),
			),
			Div(
				Id(card.ID+"-frame-save"),
				Class(saveHidden),
				Input(
					Type("submit"),
					Script(Raw(fmt.Sprintf(`( () => {
					document.getElementById('%[1]s-frame-save').onclick = function () {
						document.getElementById('%[1]s-save-front').click();
						document.getElementById('%[1]s-frame-save').classList.toggle("hidden");
						document.getElementById('%[1]s-frame-edit').classList.toggle("hidden");
					}
					document.getElementById("flip-btn-%[1]s").addEventListener("click", function(){
						console.log("Flipping card: %[1]s");
						const frontEls = document.getElementsByClassName("front-%[1]s");
						const backEls = document.getElementsByClassName("back-%[1]s");

						for (let i = 0; i < frontEls.length; i++) {
							const front = frontEls[i];
							front.classList.toggle("hidden");
						}
						for (let i = 0; i < backEls.length; i++) {
							const back = backEls[i];
							back.classList.toggle("hidden");
						}
					});
						})()`, card.ID))),
					Class("btn btn-ghost bg-gray-600 rounded-none"),
					Value("Save"),
				),
			),
		),
	)

	return frame
}

func createEditNoteCardDiv(c *NoteCard) *Node {
	editURL := fmt.Sprintf("/notecard/?cardId=%s", c.ID)
	requestType := "hx-patch"
	bgStyle := ""
	if c.ImageURL != "" {
		bgStyle = fmt.Sprintf(
			"background-image:url('%s');background-size:cover;background-position:center;",
			c.ImageURL,
		)
	}

	target := "#" + c.ID
	swap := "outerHTML"
	wrapper := func(n ...*Node) *Node {
		return Div(
			Ch(n),
		)
	}

	if c.ID == "" {
		target = "#notes"
		editURL = fmt.Sprintf("/framedcard/%s", c.ID)
		requestType = "hx-get"
		swap = "afterbegin settle:1s"
		wrapper = func(n ...*Node) *Node {
			return FramedCard(c, Div(
				Ch(n),
			))
		}
	}
	return wrapper(
		Id(c.ID),
		Class("bg-neutral-200 fade-in box-border w-[240px] aspect-[2.5/3.5] border-2 border-black rounded-lg shadow-md overflow-hidden relative"),
		Attr("style", bgStyle),

		// Front side
		Form(
			Id(c.ID+"-front"),
			Class("absolute inset-0 flex flex-col justify-between"),
			Attr(requestType, editURL),
			Attr("hx-encoding", "multipart/form-data"),
			Attr("hx-target", target),
			Attr("hx-swap", swap),

			Input(Type("hidden"), Name("cardId"), Value(c.ID)),
			Input(Type("hidden"), Name("roomID"), Value(c.RoomID)),

			// Image upload
			Div(
				Class(fmt.Sprintf("front-%[1]s bg-neutral-50 rounded-lg", c.ID)),
				Label(Class("block font-semibold bg-gray-950 text-center"), T("Update image")),
				Input(
					Type("file"),
					Name("image"),
					Class("file-input file-input-bordered w-full"),
				),
			),

			// ShortEntry
			Div(
				Class(fmt.Sprintf("front-%[1]s absolute inset-x-2 bottom-2 bg-neutral-400 opacity-75 p-2 rounded-xl", c.ID)),
				Div(
					Class("text-black text-sm whitespace-normal break-words hyphens-none text-center font-bold"),
					TextArea(
						Class("textarea textarea-bordered w-full bg-neutral-50"),
						Name("ShortEntry"),
						T(c.ShortEntry),
					),
				),
			),

			Div(
				Class(fmt.Sprintf("back-%[1]s hidden flex-1 text-black bg-neutral-400 bg-opacity-75 rounded-lg p-2 overflow-auto", c.ID)),
				TextArea(
					Class("textarea textarea-bordered w-full h-full resize-none bg-neutral-50"),
					Name("LongEntry"),
					T(c.LongEntry),
				),
			),

			// Save front
			Div(
				Class("flex justify-end mt-2 hidden"),
				Input(
					Id(c.ID+"-save-front"),
					Type("submit"),
					Class("btn btn-primary"),
					Value("Save"),
				),
			),
		),
	)
}

func createNoteCardDiv(c *NoteCard) *Node {
	bgStyle := " "
	if c.ImageURL != "" {
		bgStyle = fmt.Sprintf(
			"background-image:url('%s');background-size:cover;background-position:center;",
			c.ImageURL,
		)
	}
	entry := c.ShortEntry
	words := strings.Split(entry, " ")
	if len(words) > 35 {
		entry = c.AIEntry
	}

	return Div(
		Id(c.ID),
		Class("bg-neutral-200 fade-in box-border w-[240] aspect-[2.5/3.5] border-2 border-black rounded-lg shadow-md overflow-hidden relative"),
		Attr("style", bgStyle),
		Div(
			Class("absolute inset-0 flex flex-col justify-between"),
			// inset overlay with padding on both sides
			Div(
				Class(fmt.Sprintf("front-%[1]s absolute inset-x-2 bottom-2 bg-neutral-400 opacity-75 p-2 rounded-xl", c.ID)),
				Div(
					Class("text-black text-sm whitespace-normal break-words hyphens-none text-center font-bold"),
					T(entry),
				),
			),
			Div(
				Class(fmt.Sprintf("back-%[1]s hidden flex-1 text-black bg-neutral-400 bg-opacity-75 rounded-lg p-2 overflow-auto", c.ID)),
				TextArea(
					Class("textarea textarea-bordered w-full h-full resize-none bg-neutral-50"),
					Attr("readonly", "true"),
					Name("LongEntry"),
					T(c.LongEntry),
				),
			)),
	)
}
