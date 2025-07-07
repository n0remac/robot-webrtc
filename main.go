package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/n0remac/robot-webrtc/webrtc"
	. "github.com/n0remac/robot-webrtc/websocket"
)

const (
	webPort = ":8080"
)

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
	GameUI(mux, globalRegistry)
	ShadowReddit(mux)
	GenerateStory(mux)
	Trick(mux)
	Fantasy(mux)
	Notecard(mux, globalRegistry)

	WithWS("/ws/logs", mux, logSocketWS)

	go WsHub.Run()

	log.Println("WebRTC server started on port", webPort)
	log.Fatal(http.ListenAndServe(webPort, mux))
}

// debugEnabled toggles logging of incoming browser messages
var debugEnabled = func() bool {
	v := strings.ToLower(os.Getenv("WEBRTC_DEBUG"))
	return v == "1" || v == "true" || v == "yes"
}()

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
