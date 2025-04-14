package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

const (
	webPort = ":8080"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// Define allowed origins. In production, only noremac.dev is allowed.
		allowedOrigins := []string{"https://noremac.dev"}

		// For local development, add local origins.
		if os.Getenv("ENVIRONMENT") != "production" {
			allowedOrigins = append(allowedOrigins, "http://localhost"+webPort, "http://127.0.0.1"+webPort)
		}

		for _, allowed := range allowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func main() {
	if debugEnabled {
		log.Println("🔍 WEBRTC_DEBUG is ON – logging endpoint active")
	} else {
		log.Println("WEBRTC_DEBUG is OFF – logging endpoint will refuse connections")
	}

	// Serve static files from the 'web' directory
	fs := http.FileServer(http.Dir("./web"))
	mux := http.NewServeMux()

	mux.Handle("/video/", http.StripPrefix("/video/", fs))

	// Basic WebSocket handler
	mux.HandleFunc("/ws", handleWebsocket)

	// Newer Websocket code with command registry
	websocketRegistry := NewCommandRegistry()
	websocketHandler(websocketRegistry, mux)

	// Handle the TURN credentials endpoint
	mux.HandleFunc("/turn-credentials", handleTurnCredentials)

	// Logging endpoint
	mux.HandleFunc("/logs", handleLogSocket)

	// Apps
	Home(mux, websocketRegistry)
	ShadowReddit(mux)
	GenerateStory(mux)

	log.Println("WebRTC server started on port", webPort)
	log.Fatal(http.ListenAndServe(webPort, mux))
}
