package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// Define allowed origins. In production, only noremac.dev is allowed.
		allowedOrigins := []string{"https://noremac.dev"}

		// For local development, add local origins.
		if os.Getenv("ENVIRONMENT") != "production" {
			allowedOrigins = append(allowedOrigins, "http://localhost:8080", "http://127.0.0.1:8080")
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
	// Serve static files from the 'web' directory
	fs := http.FileServer(http.Dir("./web"))
	http.Handle("/video/", http.StripPrefix("/video/", fs))

	// Handle WebSockets separately
	http.HandleFunc("/ws", handleWebSocket)

	// Handle the TURN credentials endpoint
	http.HandleFunc("/turn-credentials", handleTurnCredentials)

	ShadowReddit()
	GenerateStory()

	fmt.Printf("Starting server at http://localhost%s\n", webPort)
	log.Fatal(http.ListenAndServe(webPort, nil))
}
