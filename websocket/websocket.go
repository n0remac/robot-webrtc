package websocket

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

type CommandFunc func(string, *Hub, map[string]interface{})

type CommandRegistry struct {
	handlers map[string]CommandFunc
	Types    map[string]interface{}
	Mu       sync.RWMutex
}

type WebsocketClient struct {
	Conn     *websocket.Conn
	Send     chan []byte
	Registry *CommandRegistry
	Room     string
	Id       string
}

type WebsocketMessage struct {
	Room    string          `json:"room"`
	Content json.RawMessage `json:"content"`
	Id      string          `json:"id"`
}

type Hub struct {
	Rooms      map[string]map[*WebsocketClient]bool
	Clients    map[*WebsocketClient]bool
	Broadcast  chan WebsocketMessage
	Register   chan *WebsocketClient
	Unregister chan *WebsocketClient
	Mu         sync.Mutex
}

var WsHub = Hub{
	Rooms:      make(map[string]map[*WebsocketClient]bool),
	Clients:    make(map[*WebsocketClient]bool),
	Broadcast:  make(chan WebsocketMessage),
	Register:   make(chan *WebsocketClient),
	Unregister: make(chan *WebsocketClient),
}

var Upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// Always allow empty origin (Playwright often omits it)
		if origin == "" {
			return true
		}

		// Accept any origin in non-production
		if os.Getenv("ENVIRONMENT") != "production" {
			return true
		}

		// Default production restriction
		return origin == "https://noremac.dev"
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		handlers: make(map[string]CommandFunc),
		Types:    make(map[string]interface{}),
	}
}

func (cr *CommandRegistry) RegisterWebsocket(command string, handler CommandFunc) {
	cr.Mu.Lock()
	defer cr.Mu.Unlock()
	cr.handlers[command] = handler
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.Mu.Lock()
			if _, ok := h.Rooms[client.Room]; !ok {
				h.Rooms[client.Room] = make(map[*WebsocketClient]bool)
			}
			h.Rooms[client.Room][client] = true
			h.Mu.Unlock()

		case client := <-h.Unregister:
			h.Mu.Lock()
			if clients, ok := h.Rooms[client.Room]; ok {
				if _, exists := clients[client]; exists {
					delete(clients, client)
					close(client.Send)
					if len(clients) == 0 {
						delete(h.Rooms, client.Room)
					}
				}
			}
			h.Mu.Unlock()

		case msg := <-h.Broadcast:
			fmt.Println("Msg received ", msg)
			h.Mu.Lock()
			if clients, ok := h.Rooms[msg.Room]; ok {
				if msg.Id == "" {
					for client := range clients {
						select {
						case client.Send <- msg.Content:
						default:
							close(client.Send)
							delete(clients, client)
						}
					}
				} else {
					for client := range clients {
						if client.Id == msg.Id {
							select {
							case client.Send <- msg.Content:
							default:
								close(client.Send)
								delete(clients, client)
							}
							break
						}
					}
				}
			}
			h.Mu.Unlock()
		}
	}
}

func (c *WebsocketClient) ReadPump() {
	defer func() {
		logInfo("client disconnected", map[string]interface{}{"room": c.Room})
		WsHub.Unregister <- c
		c.Conn.Close()
	}()

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			logError("read error", err, map[string]interface{}{"room": c.Room})
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
		handler, ok := c.Registry.handlers[typStr]
		if !ok {
			logInfo("unknown command", map[string]interface{}{"cmd": typStr, "room": c.Room})
			continue
		}
		strVal, _ := msgMap["from"].(string)
		handler(strVal, &WsHub, msgMap)
	}
}

func (c *WebsocketClient) WritePump() {
	defer func() {
		logInfo("WritePump closed", map[string]interface{}{"room": c.Room})
		c.Conn.Close()
	}()

	for message := range c.Send {
		err := c.Conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			logError("write error", err, map[string]interface{}{"room": c.Room})
			break
		}
	}
}

func WithWS(path string, mux *http.ServeMux, handler func(*websocket.Conn)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade %s → %v", path, err)
			return
		}
		log.Printf("WS %s connected", path)
		handler(conn) // delegate to feature‑specific logic
	})
}

func CreateWebsocket(registry *CommandRegistry) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		room := r.URL.Query().Get("room")
		playerId := r.URL.Query().Get("playerId")
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		client := &WebsocketClient{
			Conn:     conn,
			Send:     make(chan []byte, 256),
			Registry: registry,
			Room:     room,
			Id:       playerId,
		}
		WsHub.Register <- client
		go client.WritePump()
		client.ReadPump()
	}
}

func logInfo(msg string, meta map[string]interface{}) {
	log.Printf("[INFO] %s | %v", msg, meta)
}

func logError(msg string, err error, meta map[string]interface{}) {
	log.Printf("[ERROR] %s: %v | %v", msg, err, meta)
}
