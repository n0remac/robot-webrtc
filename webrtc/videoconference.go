package webrtc

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

	. "github.com/n0remac/robot-webrtc/websocket"
)

// TURN credential settings
var (
	coturnSecret = os.Getenv("TURN_PASS")
	coturnTTL    = int64(3600)
)

// Message is the payload for WebRTC signalling
type Message struct {
	Type      string      `json:"type"`
	Name      string      `json:"name,omitempty"`
	From      string      `json:"from,omitempty"`
	To        string      `json:"to,omitempty"`
	Offer     interface{} `json:"offer,omitempty"`
	Answer    interface{} `json:"answer,omitempty"`
	Candidate interface{} `json:"candidate,omitempty"`
}

// RobotHandler sets up the HTTP and WebSocket routes required to control the robot.
func RobotHandler(mux *http.ServeMux, registry *CommandRegistry) {
	mux.HandleFunc("/turn-credentials", handleTurnCredentials)
	mux.HandleFunc("/robot/", RobotControlHandler)

	// Register signalling commands
	registerSignallingCommands(registry)

	mux.HandleFunc("/ws/hub", CreateWebsocket(registry))
}

// registerSignallingCommands wires WebRTC commands into the Hub
func registerSignallingCommands(reg *CommandRegistry) {
	// "join": announce a new peer
	reg.RegisterWebsocket("join", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		broadcastWebRTC(room, Message{Type: "join", From: from})
	})

	// "offer": forward an SDP offer
	reg.RegisterWebsocket("offer", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to := data["to"].(string)
		fmt.Println("▶ Offer received from", from, "to", to, "in room", room)
		broadcastWebRTC(room, Message{
			Type:  "offer",
			From:  from,
			To:    to,
			Name:  data["name"].(string),
			Offer: data["offer"],
		})
	})

	// "answer": forward an SDP answer
	reg.RegisterWebsocket("answer", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to := data["to"].(string)
		broadcastWebRTC(room, Message{
			Type:   "answer",
			From:   from,
			To:     to,
			Name:   data["name"].(string),
			Answer: data["answer"],
		})
	})

	// "candidate": forward ICE candidates
	reg.RegisterWebsocket("candidate", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to := data["to"].(string)
		broadcastWebRTC(room, Message{
			Type:      "candidate",
			From:      from,
			To:        to,
			Candidate: data["candidate"],
		})
	})

	// "leave": notify peers that someone has left
	reg.RegisterWebsocket("leave", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		broadcastWebRTC(room, Message{Type: "leave", From: from})
	})
}

// getRoom extracts the room name from incoming WS data
func getRoom(data map[string]interface{}) string {
	if r, ok := data["room"].(string); ok && r != "" {
		return r
	}
	return "default"
}

// broadcastWebRTC marshals and broadcasts a signalling message into the Hub
func broadcastWebRTC(room string, msg Message) {
	fmt.Println("Broadcasting msg of type", msg.Type, " to ", msg.To)
	raw, err := json.Marshal(msg)
	if err != nil {
		log.Println("⚠️  marshal error:", err)
		return
	}
	if msg.To != "" {
		WsHub.Broadcast <- WebsocketMessage{Room: room, Content: raw, Id: msg.To}
	} else {
		WsHub.Broadcast <- WebsocketMessage{Room: room, Content: raw}
	}
}

// handleTurnCredentials issues time‐limited TURN credentials
func handleTurnCredentials(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		user = "anonymous"
	}
	username, password := generateTurnCredentials(coturnSecret, user, coturnTTL)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"username": username, "password": password})
}

// generateTurnCredentials creates a Coturn username and HMAC‐signed password
func generateTurnCredentials(secret, user string, ttlSeconds int64) (string, string) {
	expires := time.Now().Unix() + ttlSeconds
	username := fmt.Sprintf("%d:%s", expires, user)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, password
}
