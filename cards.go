package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type Lobby struct {
	Players []string
}

var lobbies = make(map[string]*Lobby)
var mu sync.Mutex

// init seeds the standard deck once
func init() {
	seedBaseDeck()
}

// GameUI registers the game routes (join screen, hand refresh, start)
func GameUI(mux *http.ServeMux, registry *CommandRegistry) {
	mux.HandleFunc("/game/", renderGamePage)
	mux.HandleFunc("/game/mvp", renderOldGamePage)
	mux.HandleFunc("/game/lobby", renderLobbyPage)
	mux.HandleFunc("/game/hand", renderHandPartial)
	mux.HandleFunc("/game/mvp/start", startGameHandler)
	mux.HandleFunc("/ws/lobby", lobbyWebsocket(registry))
}

func renderLobbyPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Name required", http.StatusBadRequest)
		return
	}

	page := DefaultLayout(
		// no /game/mvp form any more
		Script(Raw(loadFile("cards.js"))),
		Div(Class("h-screen flex flex-col bg-gray-800 text-white"),
			Div(Class("flex-1 relative"),
				Div(Id("player-top"), Class("absolute top-2 left-1/2 transform -translate-x-1/2")),
				Div(Id("player-right"), Class("absolute right-2 top-1/2 transform -translate-y-1/2")),
				Div(Id("player-bottom"), Class("absolute bottom-2 left-1/2 transform -translate-x-1/2")),
				Div(Id("player-left"), Class("absolute left-2 top-1/2 transform -translate-y-1/2")),
			),
			Button(Id("start-btn"), Class("btn btn-primary m-4"), T("Start Game")),
			Script(Raw(fmt.Sprintf(`

  (() => {
    const ws = new WebSocket("%s");
    ws.addEventListener("open", () => {
      ws.send(JSON.stringify({ type: "join", name: %q, room: %q }));
    });

    ws.addEventListener("message", ({data}) => {
      const msg = JSON.parse(data);
      if (msg.type === "lobby") {
        // positions in order: top, right, bottom, left
        ["top","right","bottom","left"].forEach((pos,i) => {
          document.getElementById("player-"+pos).innerText = msg.players[i]||"";
        });
        document.getElementById("start-btn").disabled = msg.players.length < 2;
      }
	  if (msg.type === "start") {
		window.location.href = '/game?room=%s';
	   }
    });

    document.getElementById("start-btn").addEventListener("click", () => {
      ws.send(JSON.stringify({ type: "start", room: %q }));
    });
  })();

`, websocketURL(r), name, room, room, room))),
		),
	)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func websocketURL(r *http.Request) string {
	scheme := "ws"
	if r.TLS != nil {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws/lobby?room=%s", scheme, r.Host, r.URL.Query().Get("room"))
}

func lobbyWebsocket(registry *CommandRegistry) func(http.ResponseWriter, *http.Request) {
	registry.RegisterWebsocket("joinCardGame", func(_ string, hub *Hub, data map[string]interface{}) {
		fmt.Println("join", data)
		var l Lobby
		room := data["room"].(string)

		if lobby, ok := lobbies[room]; ok {
			l = *lobby
		} else {
			l = Lobby{Players: make([]string, 0)}
		}
		if len(l.Players) >= 4 {
			log.Println("⚠️  lobby full")
			return
		}

		l.Players = append(l.Players, data["name"].(string))
		lobbies[room] = &l
		msg := map[string]interface{}{
			"type":    "lobby",
			"players": l.Players,
		}

		raw, err := json.Marshal(msg)
		if err != nil {
			log.Println("⚠️  marshal error:", err)
			return
		}
		hub.Broadcast <- WebsocketMessage{Room: room, Content: raw}
	})
	registry.RegisterWebsocket("start", func(_ string, hub *Hub, data map[string]interface{}) {
		room := data["room"].(string)
		msg := map[string]interface{}{
			"type": "start",
		}

		raw, err := json.Marshal(msg)
		if err != nil {
			log.Println("⚠️  marshal error:", err)
			return
		}

		hub.Broadcast <- WebsocketMessage{Room: room, Content: raw}
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

func joinScreen(room string) *Node {
	joinUI := Div(
		Class("flex flex-col items-center justify-center h-screen bg-gray-800 text-white space-y-4"),
		Div(Class("text-2xl"), T("Join Trick Evolution")),

		Form(
			Attr("ws-send", "submit"),
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
				"name":       "name",
			})),
			Button(
				Id("join-btn"),
				Type("submit"),
				Class("btn btn-primary w-32"), T("Join Room")),
		),
	)

	// return join page
	return Div(
		joinUI,
	)
}

// renderGamePage shows join screen or the game UI based on "name" param
func renderOldGamePage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}
	name := r.URL.Query().Get("name")

	// Base scripts (cards.js for drag/drop)
	baseOptions := Script(Raw(loadFile("cards.js")))

	// If no name, show join screen
	if name == "" {
		joinUI := Div(
			Class("flex flex-col items-center justify-center h-screen bg-gray-800 text-white space-y-4"),
			Div(Class("text-2xl"), T("Join Trick Evolution")),
			Input(Attrs(map[string]string{
				"type":        "text",
				"id":          "name-input",
				"placeholder": "Enter your name",
				"class":       "input input-bordered w-64 text-black",
			})),
			Button(Id("join-btn"), Class("btn btn-primary w-32"), T("Join Room")),
		)

		joinScript := Script(Raw(fmt.Sprintf(`
			document.addEventListener('DOMContentLoaded', function() {
			var btn = document.getElementById('join-btn');
			if (btn) {
				btn.addEventListener('click', function() {
				var name = document.getElementById('name-input').value;
				if (!name) { alert('Please enter your name.'); return }
				window.location.href = '/game/mvp?room=%s&name=' + encodeURIComponent(name);
				});
			}
			});
			`, room)))

		// Render join page
		page := DefaultLayout(
			baseOptions,
			Div(
				joinUI,
				joinScript,
			),
		)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(page.Render()))
		return
	}

	// Player joined: render game UI
	deck, hand := newGameState()
	var deckTop Card
	if len(deck) > 0 {
		deckTop = deck[0]
	}

	startBtn := Div(
		Class("p-4 bg-gray-100 text-center"),
		Button(Id("start-btn"), Class("btn btn-secondary"), T("Start Game")),
	)
	startScript := Script(Raw(fmt.Sprintf(`
		document.addEventListener('DOMContentLoaded', function() {
		var btn = document.getElementById('start-btn');
		if (btn) {
			btn.addEventListener('click', function() {
			fetch('/game/mvp/start?room=%s', { method: 'POST' })
				.then(function(resp) { if (!resp.ok) alert('Failed to start game'); });
			});
		}
		});
		`, room)))

	// Compose game UI elements
	tableArea := Div(
		Id("table-area"),
		Class("relative flex-1 bg-green-800"),
		Div(Id("deck-stack"), Class("absolute top-4 left-4"), standardCardFace(deckTop, true)),
		Div(Id("discard-pile"), Class("absolute top-4 left-20")),
	)
	handArea := Div(
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
	)

	// Render full game page
	page := DefaultLayout(
		baseOptions,
		Div(
			startScript,
			Div(
				startBtn,
				Div(Class("flex flex-col h-screen"), tableArea, handArea),
			),
		),
	)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func startGameHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}
	msg := map[string]interface{}{"type": "start"}
	raw, err := json.Marshal(msg)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	hub.Broadcast <- WebsocketMessage{Room: room, Content: raw}
	w.WriteHeader(http.StatusNoContent)
}

// renderHandPartial returns just the hand fragment for HTMX refresh
func renderHandPartial(w http.ResponseWriter, r *http.Request) {
	_, hand := newGameState()
	frag := Div(Id("player-hand"),
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
	)

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(frag.Render()))
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
