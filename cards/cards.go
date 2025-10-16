package cards

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/google/uuid"
	. "github.com/n0remac/robot-webrtc/html"
	. "github.com/n0remac/robot-webrtc/websocket"
)

type Lobby struct {
	Players []Player
}

var lobbies = make(map[string]*Lobby)
var mu sync.Mutex

type Player struct {
	Name      string
	Id        string
	Room      string
	Hand      []Card
	TricksWon int
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
				Deck:        getStandardDeck(),
				Players:     players,
				RankCompare: DefaultRankComparer,
				Phase:       PhaseStartGame,
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

		engine := RulesList()
		game.Phase = PhaseStartRound
		_ = engine.TriggerHook(HookOnStartRound, game, nil)

		// Advance to trick start phase so first card play works
		game.Phase = PhaseTrickStart
		engine.TriggerHook(HookOnPhaseEnter, game, nil)

		for _, player := range players {
			page := gameScreen(game, player.Id)

			WsHub.Broadcast <- WebsocketMessage{
				Room:    room,
				Content: []byte(page.Render()),
				Id:      player.Id,
			}
		}
		mu.Unlock()
	})

	registry.RegisterWebsocket("playCardToTrick", func(_ string, hub *Hub, data map[string]interface{}) {
		room := data["room"].(string)
		cardId := data["card"].(string)
		playerId := data["playerId"].(string)

		mu.Lock()
		defer mu.Unlock()

		game, exists := games[room]
		if !exists {
			log.Printf("⚠️  Game not found for room: %s", room)
			return
		}

		engine := RulesList()

		// 1) Validate the play
		action := GameAction{Type: "play_card", PlayerID: playerId, CardID: cardId, Room: room}
		if err := engine.ValidateAction(action, game); err != nil {
			log.Printf("🚫 Invalid play: %v", err)
			return
		}
		engine.ApplyEffects(action, game)

		// 2) Enter TrickPlay phase on first card
		if game.Phase == PhaseTrickStart {
			game.Phase = PhaseTrickPlay
			engine.TriggerHook(HookOnPhaseEnter, game, nil)
		}

		// 3) Remove card from hand
		var playedCard *Card
		for pi, p := range game.Players {
			if p.Id != playerId {
				continue
			}
			for ci, c := range p.Hand {
				if c.ID == cardId {
					playedCard = &c
					game.Players[pi].Hand = append(p.Hand[:ci], p.Hand[ci+1:]...)
					break
				}
			}
			break
		}
		if playedCard == nil {
			log.Printf("⚠️  Card %s not found in player's hand", cardId)
			return
		}

		// 4) Add to current trick
		game.PlayedCards = append(game.PlayedCards, CardPlay{PlayerID: playerId, Card: *playedCard})

		// 5) If trick complete
		if len(game.PlayedCards) == len(game.Players) {
			// End‐of‐trick rules
			effects := engine.TriggerHook(HookOnEndTrick, game, nil)
			for _, eff := range effects {
				if eff.Type == "award_trick" {
					winner := eff.Params["winner"].(string)
					log.Printf("🏆 Player %s wins the trick", winner)
					for i, p := range game.Players {
						if p.Id == winner {
							game.Players[i].TricksWon++
							break
						}
					}
				}
			}

			// Clear trick
			game.PlayedCards = nil

			// Check if round is over (all hands empty)
			if game.AllTricksPlayed() {
				// Round is complete - transition through phases
				game.Phase = PhaseTrickEnd
				engine.TriggerHook(HookOnPhaseEnter, game, nil)

				game.Phase = PhaseRoundEnd
				engine.TriggerHook(HookOnPhaseEnter, game, nil)

				// Start new round
				game.Phase = PhaseStartRound
				engine.TriggerHook(HookOnPhaseEnter, game, nil)

				game.startNewRound()

				// Advance to trick start
				game.Phase = PhaseTrickStart
				engine.TriggerHook(HookOnPhaseEnter, game, nil)

				// broadcast fresh hands and cleared trick area
				for _, p := range game.Players {
					WsHub.Broadcast <- WebsocketMessage{
						Room:    room,
						Id:      p.Id,
						Content: []byte(createPlayerHand(game, p.Id).Render()),
					}
					WsHub.Broadcast <- WebsocketMessage{
						Room:    room,
						Id:      p.Id,
						Content: []byte(createTrickArea(game, p.Id).Render()),
					}
				}
				return
			} else {
				// More tricks to play - just start next trick
				game.Phase = PhaseTrickEnd
				engine.TriggerHook(HookOnPhaseEnter, game, nil)

				game.Phase = PhaseTrickStart
				engine.TriggerHook(HookOnPhaseEnter, game, nil)
			}
		}

		// 7) Broadcast updated views
		for _, p := range game.Players {
			if p.Id == playerId {
				WsHub.Broadcast <- WebsocketMessage{
					Room:    room,
					Id:      playerId,
					Content: []byte(createPlayerHand(game, playerId).Render()),
				}
			}
			WsHub.Broadcast <- WebsocketMessage{
				Room:    room,
				Id:      p.Id,
				Content: []byte(createTrickArea(game, p.Id).Render()),
			}
		}
	})

	registry.RegisterWebsocket("discardCard", func(_ string, hub *Hub, data map[string]interface{}) {
		room := data["room"].(string)
		cardId := data["card"].(string)
		playerId := data["playerId"].(string)

		mu.Lock()
		defer mu.Unlock()

		game, exists := games[room]
		if !exists {
			log.Printf("⚠️  Game not found for room: %s", room)
			return
		}

		var playedCard *Card
		for pi, player := range game.Players {
			if player.Id != playerId {
				continue
			}

			// Remove the card from the player's hand
			for ci, c := range player.Hand {
				if c.ID == cardId {
					playedCard = &c
					// remove from hand
					game.Players[pi].Hand = append(player.Hand[:ci], player.Hand[ci+1:]...)
					break
				}
			}
			break
		}

		if playedCard == nil {
			log.Printf("⚠️  Card %s not found in player's hand", cardId)
			return
		}

		// Add to discard pile
		game.Discard = append(game.Discard, *playedCard)

		for _, player := range game.Players {
			WsHub.Broadcast <- WebsocketMessage{
				Room:    room,
				Content: []byte(createDiscardPile(game).Render()),
				Id:      player.Id,
			}
			if player.Id == playerId {
				WsHub.Broadcast <- WebsocketMessage{
					Room:    room,
					Content: []byte(createPlayerHand(game, playerId).Render()),
					Id:      player.Id,
				}
			}
		}
	})

	return CreateWebsocket(registry)
}

func renderLobbyPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		http.Error(w, "Room not specified", http.StatusBadRequest)
		return
	}
	playerId := r.URL.Query().Get("playerId")
	if playerId == "" {
		http.Error(w, "Player ID not specified", http.StatusBadRequest)
		return
	}
	lobby := lobbies[room]
	if lobby == nil {
		http.Error(w, "Lobby not found", http.StatusNotFound)
		return
	}
	var player Player
	for _, p := range lobby.Players {
		if p.Id == playerId {
			player = p
			break
		}
	}

	page := makeLobby(player)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func makeLobby(player Player) *Node {
	room := player.Room
	l := lobbies[room]
	id := player.Id

	return Div(
		Id("lobby"),
		Attr("hx-get", "/game/lobby?room="+room+"&playerId="+id),
		Attr("hx-trigger", "every 2s"),
		Attr("hx-target", "#lobby"),
		Attr("hx-swap", "innerHTML"),
		Script(Raw(fmt.Sprintf(`
			localStorage.setItem("playerId", "%s");
		`, id))),
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
	playerId := r.URL.Query().Get("playerId")

	name := r.FormValue("name")
	if name == "" {
		name = "Guest"
	}

	player := Player{
		Name: name,
		Id:   playerId,
		Room: room,
		Hand: []Card{},
	}

	mu.Lock()

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

	page := makeLobby(player)
	mu.Unlock()
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func renderGamePage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}
	playerId := uuid.NewString()
	page := DefaultLayout(
		Script(Raw(
			`let wsWrapper = null;

			document.addEventListener('htmx:wsOpen', function (evt) {
				console.log("WebSocket opened:", evt.detail);
				wsWrapper = evt.detail.socketWrapper;
			});`,
		)),
		Attr("hx-ext", "ws"),
		Div(
			Id("game-container"),
			Attr("ws-connect", fmt.Sprintf("/ws/lobby?room=%s&playerId=%s", room, playerId)),
			joinScreen(room, playerId),
		))
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page.Render()))
}

func gameScreen(game *Game, playerId string) *Node {
	deck := game.Deck

	var deckTop Card
	if len(deck) > 0 {
		deckTop = deck[0]
	}

	return DefaultLayout(
		Div(
			Script(Raw(LoadFile("cards/cards.js"))),
			Id("lobby"),
			Div(
				Div(Class("flex flex-col h-screen"),
					Div(
						Id("table-area"),
						Class("relative flex-1 bg-green-800"),
						Div(Id("deck-stack"), Class("absolute top-4 left-4"), standardCardFace(deckTop, true)),
						Div(Id("discard-pile"), Class("absolute top-4 left-20")),
						createTrickArea(game, playerId),
					),
					createPlayerHand(game, playerId),
				),
			),
		),
	)
}

func createTrickArea(game *Game, playerId string) *Node {
	return Div(
		Id("trick-area"),
		Class("absolute top-60 left-0 right-0 h-44 flex items-center justify-center gap-4"),
		Ch(func() []*Node {
			var nodes []*Node
			for _, play := range game.PlayedCards {
				card := standardCardFace(play.Card, false)
				card.Attrs["data-player-id"] = play.PlayerID
				card.Attrs["data-card-id"] = play.Card.ID
				nodes = append(nodes, card)
			}
			return nodes
		}()),
	)
}

func createPlayerHand(game *Game, playerId string) *Node {
	hand := make([]Card, 0)
	for _, player := range game.Players {
		if player.Id == playerId {
			hand = player.Hand
			break
		}
	}

	return Div(
		Id("player-hand"),
		Class("flex overflow-x-auto bg-gray-900 p-2"),
		Ch(func() []*Node {
			var nodes []*Node
			for _, c := range hand {
				n := standardCardFace(c, false)
				n.Attrs["draggable"] = "true"
				n.Attrs["ondragstart"] = "onDragStart(event)"
				n.Attrs["data-card-id"] = c.ID
				n.Attrs["data-room"] = game.Players[0].Room
				n.Attrs["data-player-id"] = playerId

				nodes = append(nodes, n)
			}
			return nodes
		}()),
	)
}

func createDiscardPile(game *Game) *Node {
	cards := game.Discard

	return Div(
		Id("discard-pile"),
		Class("absolute top-4 left-20 relative h-44 w-[300px]"),
		Ch(func() []*Node {
			var nodes []*Node
			for i, c := range cards {
				offset := fmt.Sprintf("left-[%dpx]", i*20)
				wrapper := Div(
					Class("absolute "+offset+" z-"+fmt.Sprint(100+i)),
					standardCardFace(c, false),
				)
				nodes = append(nodes, wrapper)
			}
			return nodes
		}()),
	)
}

func joinScreen(room string, playerId string) *Node {

	joinUI := Div(
		Id("lobby"),
		Class("flex flex-col items-center justify-center h-screen bg-gray-800 text-white space-y-4"),
		Div(Class("text-2xl"), T("Join Trick Evolution")),
		Form(
			Attr("hx-post", fmt.Sprintf("/game/join?room=%s&playerId=%s", room, playerId)),
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
