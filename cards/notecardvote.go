package cards

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"

	"github.com/google/uuid"
	. "github.com/n0remac/robot-webrtc/html"
	. "github.com/n0remac/robot-webrtc/websocket"
)

func registerVoting(mux *http.ServeMux, registry *CommandRegistry) {
	mux.HandleFunc("/vote/{id...}", serveVotingPage)
	mux.HandleFunc("/vote/api", handleVoteAPI)

	registry.RegisterWebsocket("notecardVoting", func(_ string, hub *Hub, data map[string]interface{}) {
		roomId := data["roomId"].(string)

		cards, err := loadCards()
		if err != nil || len(cards) == 0 {
			fmt.Println("No cards available, initializing empty slice")
			cards = []NoteCard{}
		}

		first, err := getUnvotedCard(cards, roomId)
		var page *Node
		if err != nil {
			page = Div(
				Id("main-content"),
				Class("flex flex-col items-center p-4 h-screen"),
				H1(T("No more cards available for voting")),
				P(T("Check back later or add more cards!")),
			)
		} else {
			page = Div(
				Id("main-content"),
				Attr("data-theme", "dark"),
				VoteContainer(*first, roomId),
			)
		}

		WsHub.Broadcast <- WebsocketMessage{
			Room:    roomId,
			Content: []byte(page.Render()),
		}
	})
	registry.RegisterWebsocket("notecardRanking", func(_ string, hub *Hub, data map[string]interface{}) {
		roomId := data["roomId"].(string)

		WsHub.Broadcast <- WebsocketMessage{
			Room:    roomId,
			Content: []byte(createRankingPage(roomId).Render()),
		}
	})
}

// serveVotingPage renders the swipe UI using GoDom
func serveVotingPage(w http.ResponseWriter, r *http.Request) {
	roomId := r.PathValue("id")
	if roomId == "" {
		roomId = "r" + uuid.NewString()
		http.Redirect(w, r, fmt.Sprintf("/vote/%s", roomId), http.StatusFound)
	}
	// load cards
	fmt.Println("Serving voting page for room:", roomId)
	cards, err := loadCards()
	fmt.Println("Cards Loaded")
	if err != nil || len(cards) == 0 {
		http.Error(w, "no cards available", http.StatusInternalServerError)
		cards = []NoteCard{}
	}

	first, err := getUnvotedCard(cards, roomId)
	var page *Node
	if err != nil {
		page = DefaultLayout(
			Attr("data-theme", "dark"),
			Div(
				Class("flex flex-col items-center p-4 h-screen"),
				H1(T("No more cards available for voting")),
				P(T("Check back later or add more cards!")),
			),
		)
	} else {
		page = DefaultLayout(
			Attr("data-theme", "dark"),
			VoteContainer(*first, roomId),
		)
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

// handleVoteAPI records the vote and returns the next card
func handleVoteAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cardId := r.FormValue("card_id")
	if cardId == "" {
		http.Error(w, "card_id is required", http.StatusBadRequest)
		return
	}
	vote := r.FormValue("vote")
	if vote == "" {
		http.Error(w, "vote is required", http.StatusBadRequest)
		return
	}
	roomId := r.FormValue("room_id")

	fmt.Println("Received vote:", cardId, "Vote:", vote)

	// load next card
	// TODO: A read database would be better here
	cards, err := loadCards()
	if err != nil {
		http.Error(w, "could not load cards", http.StatusInternalServerError)
		return
	}

	card, err := getCard(cardId, cards)

	if vote == "1" {
		for _, upVote := range card.UpVotes {
			if upVote == roomId {
				http.Error(w, "already voted up", http.StatusBadRequest)
				return
			}
		}
		card.UpVotes = append(card.UpVotes, roomId)
	}
	if vote == "-1" {
		for _, upVote := range card.DownVotes {
			if upVote == roomId {
				http.Error(w, "already voted down", http.StatusBadRequest)
				return
			}
		}
		card.DownVotes = append(card.DownVotes, roomId)
	}
	// TODO: A read database would be better here
	SaveCard(card)
	cards, err = loadCards()
	if err != nil {
		http.Error(w, "could not load cards", http.StatusInternalServerError)
		return
	}

	first, err := getUnvotedCard(cards, roomId)
	var page *Node
	if err != nil {
		page = DefaultLayout(
			Attr("data-theme", "dark"),
			Div(
				Class("flex flex-col items-center p-4 h-screen"),
				H1(T("No more cards available for voting")),
				P(T("Check back later or add more cards!")),
			),
		)
	} else {
		page = VoteContainer(*first, roomId)
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))

}

func VoteContainer(card NoteCard, roomId string) *Node {
	return Div(
		Id("vote-container"),
		Class("flex flex-col items-center"),

		// the card display
		createNoteCardDiv(&card),

		// swipe buttons
		Div(
			Class("flex flex-row buttons mt-4"),
			Form(
				Attr("hx-post", "/vote/api"),
				Attr("hx-swap", "outerHTML"),
				Attr("hx-target", "#vote-container"),
				Input(
					Type("hidden"),
					Name("card_id"),
					Value(card.ID),
				),
				Input(
					Type("hidden"),
					Name("vote"),
					Value("-1"),
				),
				Input(
					Type("hidden"),
					Name("room_id"),
					Value(roomId),
				),
				Input(
					Type("submit"),
					Id("down"),
					Value("👎"),
					Class("btn btn-outline btn-lg mr-4"),
				),
			),
			Form(
				Attr("hx-post", "/vote/api"),
				Attr("hx-swap", "outerHTML"),
				Attr("hx-target", "#vote-container"),
				Input(
					Type("hidden"),
					Name("card_id"),
					Value(card.ID),
				),
				Input(
					Type("hidden"),
					Name("vote"),
					Value("1"),
				),
				Input(
					Type("hidden"),
					Name("room_id"),
					Value(roomId),
				),
				Input(
					Type("submit"),
					Id("up"),
					Value("👍"),
					Class("btn btn-outline btn-lg mr-4"),
				),
			),
		),

		// embed the voting script
		Script(Raw(fmt.Sprintf(`
				let currentIndex = 0;
				let currentCardID = "%s";

				function vote(v) {
					
				}

				// attach events
				document.getElementById("down").onclick = () => vote(-1);
				document.getElementById("up").onclick   = () => vote( 1);
				document.onkeydown = e => {
					if (e.key === "ArrowLeft") vote(-1);
					if (e.key === "ArrowRight") vote(1);
				};

				let startX = 0;
				const cardEl = document.getElementById("card");
				cardEl.addEventListener("touchstart", e => startX = e.touches[0].screenX);
				cardEl.addEventListener("touchend", e => {
					let dx = e.changedTouches[0].screenX - startX;
					if (dx > 50) vote(1);
					if (dx < -50) vote(-1);
				});
			`, card.ID))),
	)
}

func getUnvotedCard(cards []NoteCard, roomId string) (*NoteCard, error) {
	for _, c := range cards {
		var allVoters []string
		allVoters = append(c.UpVotes, c.DownVotes...)
		if slices.Contains(allVoters, roomId) {
			continue // skip cards already voted on by this room
		} else {
			return &c, nil // return the first card that hasn't been voted on
		}
	}
	return nil, fmt.Errorf("no unvoted cards available")
}

func getCard(ID string, cards []NoteCard) (*NoteCard, error) {
	if len(cards) == 0 {
		cards, err := loadCards()
		if err != nil {
			return nil, fmt.Errorf("could not load cards: %w", err)
		}
		if len(cards) == 0 {
			return nil, fmt.Errorf("no cards available")
		}
	}
	for _, card := range cards {
		if card.ID == ID {
			return &card, nil
		}
	}
	return nil, fmt.Errorf("card not found: %s", ID)
}

// loadCards reads cards.json into a slice
func loadCards() ([]NoteCard, error) {
	data, err := os.ReadFile(cardsFilePath)
	if err != nil {
		return nil, err
	}
	var cs []NoteCard
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// NoteCard helper for templating
func (c *NoteCard) AIEntryOrEntry() string {
	if c.AIEntry != "" {
		return c.AIEntry
	}
	return c.ShortEntry
}

func createRankingPage(roomId string) *Node {
	cards, err := loadCards()
	if err != nil {
		fmt.Println("No cards available, initializing empty slice")
		cards = []NoteCard{}
	}

	// sort descending by len(UpVotes)
	sort.Slice(cards, func(i, j int) bool {
		return len(cards[i].UpVotes) > len(cards[j].UpVotes)
	})

	// Create the page with existing cards
	cardDivs := make([]*Node, 0, len(cards))
	for _, card := range cards {
		cardDivs = append(cardDivs, Div(
			createNoteCardDiv(&card),
			P(
				Class("mt-2"),
				T(fmt.Sprintf("👍 %d 👎 %d", len(card.UpVotes), len(card.DownVotes))),
			),
		))
	}

	return Div(
		Id("main-content"),
		Class("container mx-auto p-6"),
		H1(Class("text-3xl font-bold mb-6"), T("🏆 Card Rankings")),
		Div(
			Id("notes"),
			Attr("hx-swap-oob", "beforeend"),
			Class("space-y-4 flex flex-col items-center"),
			Ch(cardDivs),
		),
	)
}
