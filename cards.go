package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Lobby struct {
	Players []Player
}

var lobbies = make(map[string]*Lobby)
var mu sync.Mutex

type Player struct {
	Name string
	Id   string
	Room string
	Hand []Card
}

type Game struct {
	mu          sync.Mutex
	Deck        []Card
	Discard     []Card
	Players     []Player
	CurrentTurn int
	Started     bool
}

var games = make(map[string]*Game)

// init seeds the standard deck once
func init() {
	seedBaseDeck()
}

// GameUI registers the game routes (join screen, hand refresh, start)
func GameUI(mux *http.ServeMux, registry *CommandRegistry) {
	mux.HandleFunc("/game/", renderGamePage)
	mux.HandleFunc("/game/join", joinLobby)
	mux.HandleFunc("/game/lobby", renderLobbyPage)
	mux.HandleFunc("/ws/lobby", lobbyWebsocket(registry))
}

func lobbyWebsocket(registry *CommandRegistry) func(http.ResponseWriter, *http.Request) {
	registry.RegisterWebsocket("startCardGame", func(_ string, hub *Hub, data map[string]interface{}) {
		room := data["room"].(string)
		fmt.Println("Starting game in room:", room)

		lobby := lobbies[room]
		if lobby == nil {
			log.Println("⚠️  No lobby found")
			return
		}
		players := lobby.Players

		mu.Lock()
		game, exists := games[room]
		if !exists {
			game = &Game{
				Deck:    getStandardDeck(),
				Players: players,
			}
			games[room] = game
		}

		dealNum := 5
		// deal 5 cards to each player
		if len(game.Deck) < dealNum*len(players) {
			log.Println("⚠️  Not enough cards to deal")
			return
		}
		for range dealNum {
			for player := range players {
				card := game.Deck[0]
				game.Deck = game.Deck[1:]
				game.Players[player].Hand = append(game.Players[player].Hand, card)
			}
		}

		mu.Unlock()

		page := gameScreen(game)

		hub.Broadcast <- WebsocketMessage{Room: room, Content: []byte(page.Render())}
	})

	return func(w http.ResponseWriter, r *http.Request) {
		room := r.URL.Query().Get("room")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		client := &WebsocketClient{
			conn:     conn,
			send:     make(chan []byte, 256),
			registry: registry,
			room:     room,
		}
		hub.register <- client
		go client.writePump()
		client.readPump()
	}
}

func renderLobbyPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		http.Error(w, "Room not specified", http.StatusBadRequest)
		return
	}

	page := makeLobby(room)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func makeLobby(room string) *Node {
	l := lobbies[room]

	return Div(
		Id("lobby"),
		Attr("hx-get", "/game/lobby?room="+room),
		Attr("hx-trigger", "every 2s"),
		Attr("hx-target", "#lobby"),
		Attr("hx-swap", "innerHTML"),
		Class("flex flex-col items-center justify-center h-screen bg-gray-800 text-white space-y-4"),
		Div(Class("text-2xl"), T("Lobby")),
		Div(Class("text-lg"), T("Players:")),
		Div(Id("player-list"), Class("text-lg"), Ch(func() []*Node {
			var nodes []*Node
			for _, player := range l.Players {
				nodes = append(nodes, Div(Class("text-lg"), T(player.Name)))
			}
			return nodes
		}())),
		Form(
			Attr("ws-send", "submit"),
			Input(
				Type("hidden"),
				Name("type"),
				Value("startCardGame"),
			),
			Input(
				Type("hidden"),
				Name("room"),
				Value(room),
			),
			//player names
			Ch(func() []*Node {
				var nodes []*Node
				for _, player := range l.Players {
					nodes = append(nodes, Input(
						Type("hidden"),
						Name("player"),
						Value(player.Name),
					))
				}
				return nodes
			}()),
			Input(
				Type("submit"),
				Class("btn btn-primary w-32"),
				Value("Start Game"),
			),
		),
	)
}

func joinLobby(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")

	name := r.FormValue("name")
	if name == "" {
		name = "Guest"
	}

	player := Player{
		Name: name,
		Id:   uuid.NewString(),
		Room: room,
		Hand: []Card{},
	}

	mu.Lock()
	defer mu.Unlock()
	var l Lobby

	if lobby, ok := lobbies[room]; ok {
		l = *lobby
	} else {
		l = Lobby{Players: make([]Player, 0)}
	}
	if len(l.Players) >= 4 {
		log.Println("⚠️  lobby full")
		return
	}

	l.Players = append(l.Players, player)
	lobbies[room] = &l
	mu.Unlock()

	page := makeLobby(room)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func renderGamePage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	page := DefaultLayout(
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/ws/hub?room="+room),
		joinScreen(room),
	)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func gameScreen(game *Game) *Node {
	room := game.Players[0].Room
	deck, hand := newGameState()
	var deckTop Card
	if len(deck) > 0 {
		deckTop = deck[0]
	}

	return DefaultLayout(
		Script(Raw(loadFile("cards.js"))),
		Div(
			Id("lobby"),
			Script(Raw(fmt.Sprintf(`
				document.addEventListener('DOMContentLoaded', function() {
				var btn = document.getElementById('start-btn');
				if (btn) {
					btn.addEventListener('click', function() {
					fetch('/game/mvp/start?room=%s', { method: 'POST' })
						.then(function(resp) { if (!resp.ok) alert('Failed to start game'); });
					});
				}
				});
			`, room))),
			Div(
				Div(
					Class("p-4 bg-gray-100 text-center"),
					Button(Id("start-btn"), Class("btn btn-secondary"), T("Start Game")),
				),
				Div(Class("flex flex-col h-screen"),
					Div(
						Id("table-area"),
						Class("relative flex-1 bg-green-800"),
						Div(Id("deck-stack"), Class("absolute top-4 left-4"), standardCardFace(deckTop, true)),
						Div(Id("discard-pile"), Class("absolute top-4 left-20")),
					),
					Div(
						Id("player-hand"),
						Class("flex overflow-x-auto bg-gray-900 p-2"),
						Ch(func() []*Node {
							var nodes []*Node
							for _, c := range hand {
								n := standardCardFace(c, false)
								n.Attrs["draggable"] = "true"
								n.Attrs["ondragstart"] = "onDragStart(event)"
								n.Attrs["data-card-id"] = c.ID

								nodes = append(nodes, n)
							}
							return nodes
						}()),
					)),
			),
		),
	)
}

func joinScreen(room string) *Node {
	joinUI := Div(
		Id("lobby"),
		Class("flex flex-col items-center justify-center h-screen bg-gray-800 text-white space-y-4"),
		Div(Class("text-2xl"), T("Join Trick Evolution")),
		Form(
			Attr("hx-post", "/game/join?room="+room),
			Attr("hx-target", "#lobby"),
			Attr("hx-swap", "innerHTML"),
			Input(
				Type("hidden"),
				Name("type"),
				Value("joinCardGame"),
			),
			Input(
				Type("hidden"),
				Name("room"),
				Value(room),
			),
			Input(Attrs(map[string]string{
				"type":        "text",
				"id":          "name-input",
				"placeholder": "Enter your name",
				"class":       "input input-bordered w-64 text-black",
				"name":        "name",
			})),
			Input(
				Type("submit"),
				Id("join-btn"),
				Class("btn btn-primary w-32"),
				Value("Join Room"),
			),
		),
	)

	// return join page
	return Div(
		joinUI,
	)
}

// newGameState returns a shuffled standard deck and a starting hand of 5 cards
func newGameState() ([]Card, []Card) {
	all := getStandardDeck()
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	handSize := 5
	if len(all) < handSize+1 {
		handSize = len(all) / 2
	}
	hand := make([]Card, handSize)
	copy(hand, all[:handSize])
	deck := make([]Card, len(all)-handSize)
	copy(deck, all[handSize:])
	return deck, hand
}

// getStandardDeck pulls all standard (52-card) cards from the in-memory store
func getStandardDeck() []Card {
	store.RLock()
	defer store.RUnlock()
	var out []Card
	for _, c := range store.m {
		if c.Cat == CategoryStandard {
			out = append(out, c)
		}
	}
	return out
}
