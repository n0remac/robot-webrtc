package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
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
}

type WebsocketMessage struct {
	Room    string
	Content []byte
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
				for client := range clients {
					select {
					case client.send <- msg.Content:
					default:
						close(client.send)
						delete(clients, client)
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

func (c *WebsocketClient) readPump(w http.ResponseWriter, r *http.Request) {
	defer func() {
		hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			log.Println("ReadMessage error:", err)
			break
		}

		var msgMap map[string]interface{}
		err = json.Unmarshal(message, &msgMap)
		if err != nil {
			log.Println("JSON Unmarshal error:", err)
			return
		}
		delete(msgMap, "HEADERS")

		for key, value := range msgMap {
			c.registry.mu.RLock()
			handler, ok := c.registry.handlers[key]
			c.registry.mu.RUnlock()
			if ok {
				handler(value.(string), &hub, msgMap)
			}
		}
	}
}

func (c *WebsocketClient) writePump() {
	defer c.conn.Close()
	for message := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			log.Println("WriteMessage error:", err)
			break
		}
	}
}

func websocketHandler(registry *CommandRegistry, mux *http.ServeMux) {
	go hub.run()

	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		// Extract room from query parameters
		queryParams, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			log.Println("Error parsing query parameters:", err)
			return
		}

		room := queryParams.Get("room")
		if room == "" {
			room = "default" // Fallback room if none is provided
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}

		client := &WebsocketClient{
			conn:     conn,
			send:     make(chan []byte, 256),
			registry: registry,
			room:     room,
		}
		hub.register <- client

		go client.writePump()
		client.readPump(w, r)
	})
}
