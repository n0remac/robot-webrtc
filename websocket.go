package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type CommandFunc func(string, *Hub, map[string]interface{})

type CommandRegistry struct {
	handlers map[string]CommandFunc
	Types    map[string]interface{}
	mu       sync.RWMutex
}

type WebsocketClient struct {
	conn     *websocket.Conn
	send     chan []byte
	registry *CommandRegistry
	room     string
	id       string
}

type WebsocketMessage struct {
	Room    string          `json:"room"`
	Content json.RawMessage `json:"content"`
	Id      string          `json:"id"`
}

type Hub struct {
	rooms      map[string]map[*WebsocketClient]bool
	clients    map[*WebsocketClient]bool
	Broadcast  chan WebsocketMessage
	register   chan *WebsocketClient
	unregister chan *WebsocketClient
	mu         sync.Mutex
}

var hub = Hub{
	rooms:      make(map[string]map[*WebsocketClient]bool),
	clients:    make(map[*WebsocketClient]bool),
	Broadcast:  make(chan WebsocketMessage),
	register:   make(chan *WebsocketClient),
	unregister: make(chan *WebsocketClient),
}

func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		handlers: make(map[string]CommandFunc),
		Types:    make(map[string]interface{}),
	}
}

func (cr *CommandRegistry) RegisterWebsocket(command string, handler CommandFunc) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.handlers[command] = handler
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if _, ok := h.rooms[client.room]; !ok {
				h.rooms[client.room] = make(map[*WebsocketClient]bool)
			}
			h.rooms[client.room][client] = true
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.rooms[client.room]; ok {
				if _, exists := clients[client]; exists {
					delete(clients, client)
					close(client.send)
					if len(clients) == 0 {
						delete(h.rooms, client.room)
					}
				}
			}
			h.mu.Unlock()

		case msg := <-h.Broadcast:
			h.mu.Lock()
			if clients, ok := h.rooms[msg.Room]; ok {
				if msg.Id == "" {
					for client := range clients {
						select {
						case client.send <- msg.Content:
						default:
							close(client.send)
							delete(clients, client)
						}
					}
				} else {
					for client := range clients {
						if client.id == msg.Id {
							select {
							case client.send <- msg.Content:
							default:
								close(client.send)
								delete(clients, client)
							}
							break
						}
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

func (c *WebsocketClient) readPump() {
	defer func() {
		logInfo("client disconnected", map[string]interface{}{"room": c.room})
		hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			logError("read error", err, map[string]interface{}{"room": c.room})
			break
		}

		var msgMap map[string]interface{}
		if err := json.Unmarshal(message, &msgMap); err != nil {
			logError("JSON unmarshal failed", err, map[string]interface{}{"raw": string(message)})
			return
		}

		typ := msgMap["type"]
		if typ == nil {
			logError("missing type", nil, map[string]interface{}{"raw": string(message)})
			continue
		}
		typStr, ok := typ.(string)
		if !ok {
			logError("type not string", nil, map[string]interface{}{"raw": string(message)})
			continue
		}
		handler, ok := c.registry.handlers[typStr]
		if !ok {
			logInfo("unknown command", map[string]interface{}{"cmd": typStr, "room": c.room})
			continue
		}
		strVal, _ := msgMap["from"].(string)
		handler(strVal, &hub, msgMap)
	}
}

func (c *WebsocketClient) writePump() {
	defer func() {
		logInfo("writePump closed", map[string]interface{}{"room": c.room})
		c.conn.Close()
	}()

	for message := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			logError("write error", err, map[string]interface{}{"room": c.room})
			break
		}
	}
}

func withWS(path string, mux *http.ServeMux, handler func(*websocket.Conn)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade %s → %v", path, err)
			return
		}
		log.Printf("WS %s connected", path)
		handler(conn) // delegate to feature‑specific logic
	})
}

func createWebsocket(registry *CommandRegistry) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		room := r.URL.Query().Get("room")
		playerId := r.URL.Query().Get("playerId")
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
			id:       playerId,
		}
		hub.register <- client
		go client.writePump()
		client.readPump()
	}
}
