package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

var clients = make(map[*websocket.Conn]bool)
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
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func main() {
	// Serve static files from the 'web' directory
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/", fs)

	// Explicitly serve JS and CSS
	http.Handle("/app.js", http.FileServer(http.Dir("./web")))
	http.Handle("/style.css", http.FileServer(http.Dir("./web")))

	// Handle WebSockets separately
	http.HandleFunc("/ws", handleWebSocket)

	fmt.Printf("Starting server at http://localhost%s\n", webPort)
	log.Fatal(http.ListenAndServe(webPort, nil))
}

// Handles WebSocket messages and broadcasts them
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()

	clients[conn] = true

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Error reading message:", err)
			delete(clients, conn)
			break
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Println("Error unmarshaling message:", err)
			continue
		}

		// Broadcast the message to all clients except sender
		for client := range clients {
			if client != conn {
				err := client.WriteJSON(msg)
				if err != nil {
					log.Println("Error writing JSON:", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
	}
}

func createPeerConnection() (*webrtc.PeerConnection, error) {
	// Define ICE servers
	iceServers := []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	}

	// Create a new RTCPeerConnection
	config := webrtc.Configuration{
		ICEServers: iceServers,
	}
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	// Handle ICE connection state changes
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", state.String())
	})

	return peerConnection, nil
}
