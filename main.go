package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var clients = make(map[*websocket.Conn]string)
var broadcast = make(chan Message)

type Message struct {
	Type      string      `json:"type"`
	Name      string      `json:"name,omitempty"`
	Offer     interface{} `json:"offer,omitempty"`
	Answer    interface{} `json:"answer,omitempty"`
	Candidate interface{} `json:"candidate,omitempty"`
}

// Define constants and variables
const (
	webPort = ":8080"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func main() {
	// Serve static files from the 'web' directory
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)

	// Handle WebSockets separately
	http.HandleFunc("/ws", handleWebSocket)

	fmt.Printf("Starting server at http://localhost%s\n", webPort)
	log.Fatal(http.ListenAndServe(webPort, nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	clients[conn] = "" // Initialize empty username
	log.Println("✅ New user connected")

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("⚠️ User disconnected:", err)

			userName, exists := clients[conn] // Retrieve the correct username
			if exists {
				delete(clients, conn) // Remove user from map
			}

			// Broadcast "leave" message with the correct user name
			if userName != "" {
				leaveMessage := Message{Type: "leave", Name: userName}
				for client := range clients {
					log.Println("👋 Broadcasting leave message for", userName)
					client.WriteJSON(leaveMessage)
				}
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Println("❌ JSON Unmarshal error:", err)
			continue
		}

		// Store the username properly in the `clients` map
		if msg.Type == "join" {
			clients[conn] = msg.Name
			log.Println("🆕 User joined:", msg.Name)
		}

		// Broadcast the message to all other clients
		for client := range clients {
			if client != conn {
				err := client.WriteJSON(msg)
				if err != nil {
					log.Println("⚠️ WebSocket write error:", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
	}
}
