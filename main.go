package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/n0remac/robot-webrtc/webrtc"
	. "github.com/n0remac/robot-webrtc/websocket"
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/robot/", http.StatusFound)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	RobotHandler(mux, globalRegistry)

	WithWS("/ws/logs", mux, logSocketWS)

	go WsHub.Run()

	port := envOrDefault("PORT", "8080")
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
	}()

	log.Println("WebRTC server listening on", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
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
