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
	
		// Always allow empty origin (Playwright often omits it)
		if origin == "" {
			return true
		}
	
		// Accept any origin in non-production
		if os.Getenv("ENVIRONMENT") != "production" {
			return true
		}
	
		// Default production restriction
		return origin == "https://noremac.dev"
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

	// Create a new HTTP server
	mux := http.NewServeMux()
	// create global registry
	globalRegistry := NewCommandRegistry()

	// Apps
	Home(mux, globalRegistry)
	VideoHandler(mux, globalRegistry)
	ShadowReddit(mux)
	GenerateStory(mux)
	Trick(mux)
	Fantasy(mux)

	go hub.run()

	log.Println("WebRTC server started on port", webPort)
	log.Fatal(http.ListenAndServe(webPort, mux))
}


func withWS(path string, mux *http.ServeMux, handler func(*websocket.Conn)) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade %s → %v", path, err)
			return
		}
		log.Printf("WS %s connected", path)
		handler(conn) // delegate to feature‑specific logic
	})
}