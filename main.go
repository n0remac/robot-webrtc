package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

var clients = make(map[*websocket.Conn]string)
var broadcast = make(chan Message)

type Message struct {
	Type      string      `json:"type"`
    UUID      string      `json:"uuid,omitempty"`
	Name      string      `json:"name,omitempty"`
	Offer     interface{} `json:"offer,omitempty"`
	Answer    interface{} `json:"answer,omitempty"`
	Candidate interface{} `json:"candidate,omitempty"`
}

// Define constants and variables
const (
	webPort = ":8080"
)

var (
	coturnSecret = os.Getenv("TURN_PASS")
	coturnTTL    = int64(3600)
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

	// Handle the TURN credentials endpoint
	http.HandleFunc("/turn-credentials", handleTurnCredentials)

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

func handleTurnCredentials(w http.ResponseWriter, r *http.Request) {
	// In a real app, you might retrieve a user identifier from session/cookie/etc.
	// For example, if you want to tie it to a username, do something like:
	// user := r.URL.Query().Get("user")
	// If none provided, default to "anonymous".
	user := "anonymous"

	username, password := generateTurnCredentials(coturnSecret, user, coturnTTL)

	// Return JSON, e.g.: { "username": "...", "password": "..." }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"username": username,
		"password": password,
	})
}

func generateTurnCredentials(secret, user string, ttlSeconds int64) (string, string) {
	// Expire time
	expires := time.Now().Unix() + ttlSeconds

	// Turn username format: "expires:username"
	username := fmt.Sprintf("%d:%s", expires, user)

	// Create HMAC-SHA1 of `username` with your static-auth-secret
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return username, password
}
