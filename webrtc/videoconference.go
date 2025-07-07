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

	. "github.com/n0remac/robot-webrtc/html"
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

// VideoHandler sets up the HTTP and WebSocket routes for video
func VideoHandler(mux *http.ServeMux, registry *CommandRegistry) {
	// Page and TURN credentials
	mux.HandleFunc("/video/", VideoPage)
	mux.HandleFunc("/turn-credentials", handleTurnCredentials)
	mux.HandleFunc("/robot/", RobotControlHandler)

	// Register signalling commands
	registerSignallingCommands(registry)

	// WebSocket endpoints
	mux.HandleFunc("/ws/hub", func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade /ws/hub → %v", err)
			return
		}
		room := r.URL.Query().Get("room")
		if room == "" {
			room = "default"
		}
		client := &WebsocketClient{
			Conn:     conn,
			Send:     make(chan []byte, 256),
			Registry: registry,
			Room:     room,
		}
		WsHub.Register <- client
		go client.WritePump()
		client.ReadPump()
	})
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

// VideoPage renders the HTML layout for the video client
func VideoPage(w http.ResponseWriter, r *http.Request) {
	// Determine room from query or default
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}

	page := DefaultLayout(
		Style(Raw(LoadFile("webrtc/video.css"))),
		Script(Raw(LoadFile("webrtc/logger.js"))),
		Script(Raw(LoadFile("webrtc/video.js"))),
		Script(Raw(LoadFile("webrtc/media-controls.js"))),
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/ws/hub?room="+room),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center min-h-screen",
			"data-theme": "dark",
		}),
			// Join screen with room selector and device tests
			Div(
				Id("join-screen"), Class("mt-24 flex flex-col items-center space-y-4"),
				Input(Attrs(map[string]string{
					"type":        "text",
					"id":          "name",
					"placeholder": "Your name",
					"class":       "border rounded px-2 py-1 w-64",
				})),
				Input(Attrs(map[string]string{
					"type":        "text",
					"id":          "room",
					"placeholder": room,
					"class":       "border rounded px-2 py-1 w-64",
				})),
				// Device test buttons
				Div(Class("space-x-2"),
					Button(Id("test-camera"), Class("btn btn-sm"), T("Test Camera")),
					Button(Id("test-mic"), Class("btn btn-sm"), T("Test Microphone")),
				),
				// Preview video element (hidden until camera test)
				Raw(`<video id="preview-video" style="display:none; width:320px; height:240px; border:1px solid #444; border-radius:4px;" autoplay playsinline muted></video>`),
				// Mic status display
				Div(Id("mic-status"), Class("text-sm text-gray-300")),
				// Join button
				Button(Id("join-btn"), Class("btn mt-2 w-32"), T("Join")),
			),
			// Participant view remains unchanged
			Div(Id("participant-view"), Attr("style", "display:none;"), Class("mt-6"),
				Div(Id("videos"), Class("relative grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-4 w-full h-full p-4")),
				Div(Id("controls"), Class("mt-5 space-x-4"),
					Button(Id("mute-btn"), Class("btn btn-sm"), T("Mute")),
					Button(Id("video-btn"), Class("btn btn-sm"), T("Stop Video")),
					Button(Id("noise-btn"), Class("btn btn-sm"), T("Noise Suppression")),
					Span(Id("w-status"), Class("text-sm text-gray-300"), T("W: up")),
				),
			),
		),
	)

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(page.Render()))
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
