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
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// debugEnabled toggles logging of incoming browser messages
var debugEnabled = func() bool {
	v := strings.ToLower(os.Getenv("WEBRTC_DEBUG"))
	return v == "1" || v == "true" || v == "yes"
}()

// TURN credential settings
var (
	coturnSecret = os.Getenv("TURN_PASS")
	coturnTTL    = int64(3600)
)

// Message is the payload for WebRTC signalling
type Message struct {
	Type      string      `json:"type"`
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

	// Register signalling commands
	registerSignallingCommands(registry)

	// WebSocket endpoints
	mux.HandleFunc("/ws/hub", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade /ws/hub → %v", err)
			return
		}
		room := r.URL.Query().Get("room")
		if room == "" {
			room = "default"
		}
		client := &WebsocketClient{
			conn:     conn,
			send:     make(chan []byte, 256),
			registry: registry,
			room:     room,
		}
		hub.register <- client
		go client.writePump()
		client.readPump()
	})

	withWS("/ws/logs", mux, logSocketWS)
}

// registerSignallingCommands wires WebRTC commands into the Hub
func registerSignallingCommands(reg *CommandRegistry) {
	// "join": announce a new peer
	reg.RegisterWebsocket("join", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		broadcastWebRTC(hub, room, Message{Type:"join", From:from})
	  })

	// "offer": forward an SDP offer
	reg.RegisterWebsocket("offer", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to   := data["to"].(string)
		broadcastWebRTC(hub, room, Message{
		  Type:      "offer",
		  From:      from,
		  To:        to,
		  Offer:     data["offer"],
		})
	  })
	  

	// "answer": forward an SDP answer
	reg.RegisterWebsocket("answer", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to   := data["to"].(string)
		broadcastWebRTC(hub, room, Message{
			Type:   "answer",
			From:      from,
			To:        to,
			Answer: data["answer"],
		})
	})

	// "candidate": forward ICE candidates
	reg.RegisterWebsocket("candidate", func(_ string, hub *Hub, data map[string]interface{}) {
		room := getRoom(data)
		from := data["from"].(string)
		to   := data["to"].(string)
		broadcastWebRTC(hub, room, Message{
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
		broadcastWebRTC(hub, room, Message{Type:"leave", From:from})
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
func broadcastWebRTC(hub *Hub, room string, msg Message) {
	raw, err := json.Marshal(msg)
	if err != nil {
		log.Println("⚠️  marshal error:", err)
		return
	}
	hub.Broadcast <- WebsocketMessage{Room: room, Content: raw}
}

// VideoPage renders the HTML layout for the video client
func VideoPage(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}

	page := DefaultLayout(
		Style(Raw(loadFile("video.css"))),
		Script(Raw(loadFile("video.js"))),
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/ws/hub?room="+room),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center min-h-screen",
			"data-theme": "dark",
		}),
			// join screen
			Div(Id("join-screen"), Class("mt-24"),
				Input(Attrs(map[string]string{
					"type":        "text",
					"id":          "name",
					"placeholder": "Enter your name",
					"class":       "border rounded px-2 py-1",
				})),
				Button(Id("join-btn"), Class("ml-2 btn"), T("Join")),
			),

			// participant view
			Div(Id("participant-view"), Attr("style", "display:none;"), Class("mt-6"),
				Div(Id("videos"), Class("relative flex justify-center items-center w-full h-full")),
				Div(Id("controls"), Class("mt-5 space-x-4"),
					Button(Id("mute-btn"), Class("btn btn-sm"), T("Mute")),
					Button(Id("video-btn"), Class("btn btn-sm"), T("Stop Video")),
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

// logSocketWS streams browser logs to both file and stdout
func logSocketWS(conn *websocket.Conn) {
	if !debugEnabled {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "logging disabled"))
		conn.Close()
		return
	}
	defer conn.Close()

	// Ensure log directory
	if err := os.MkdirAll("serverlogs", 0755); err != nil {
		log.Printf("mkdir serverlogs error: %v", err)
		return
	}

	// Open append‐only daily log
	fileName := fmt.Sprintf("serverlogs/%s.webrtc.log", time.Now().Format("2006-01-02"))
	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("open log file error: %v", err)
		return
	}
	defer f.Close()

	log.Printf("📝 log‐socket connected → %s", fileName)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("log‐socket closed: %v", err)
			return
		}
		// write and mirror
		f.Write(append(raw, '\n'))
		log.Printf("[browser] %s", raw)
	}
}