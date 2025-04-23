package main

import (
	"encoding/json"
	"log"
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
	Room    string          `json:"room"`
	Content json.RawMessage `json:"content"`
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

func (c *WebsocketClient) readPump() {
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
			if !ok {
				continue
			}
			strVal, _ := value.(string)
			handler(strVal, &hub, msgMap)
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

func hubWS(globalRegistry *CommandRegistry) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		// pull ?room=<name> from the query
		room := "default"
		if q, err := url.ParseQuery(conn.LocalAddr().String()); err == nil {
			if r := q.Get("room"); r != "" {
				room = r
			}
		}

		client := &WebsocketClient{
			conn:     conn,
			send:     make(chan []byte, 256),
			registry: globalRegistry,
			room:     room,
		}
		hub.register <- client
		go client.writePump()
		client.readPump()
	}
}
