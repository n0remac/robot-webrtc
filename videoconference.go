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

var debugEnabled = func() bool {
	v := strings.ToLower(os.Getenv("WEBRTC_DEBUG"))
	return v == "1" || v == "true" || v == "yes"
}()

var clients = make(map[*websocket.Conn]string)
var broadcast = make(chan Message)

type Message struct {
	Type      string      `json:"type"`
	UUID      string      `json:"uuid,omitempty"`
	Offer     interface{} `json:"offer,omitempty"`
	Answer    interface{} `json:"answer,omitempty"`
	Candidate interface{} `json:"candidate,omitempty"`
	Enable    bool        `json:"enable,omitempty"`
}

// Define constants and variables

var (
	coturnSecret = os.Getenv("TURN_PASS")
	coturnTTL    = int64(3600)
)

func handleWebsocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	clients[conn] = "" // Initialize empty userUUID
	log.Println("✅ New user connected")

	if debugEnabled {
		_ = conn.WriteJSON(Message{Type: "debug", Enable: true})
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("⚠️ User disconnected:", err)

			userUUID, exists := clients[conn] // Retrieve the correct userUUID
			if exists {
				delete(clients, conn) // Remove user from map
			}

			// Broadcast "leave" message with the correct user name
			if userUUID != "" {
				leaveMessage := Message{Type: "leave", UUID: userUUID}
				for client := range clients {
					log.Println("👋 Broadcasting leave message for", userUUID)
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

		// Store the userUUID properly in the `clients` map
		if msg.Type == "join" {
			clients[conn] = msg.UUID
			log.Println("🆕 User joined:", msg.UUID)
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

func handleLogSocket(w http.ResponseWriter, r *http.Request) {
    // If debug is OFF simply refuse the connection with 403
    if !debugEnabled {
        http.Error(w, "logging disabled", http.StatusForbidden)
        return
    }

    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Println("log‑socket upgrade error:", err)
        return
    }
    defer conn.Close()

    // open (or create) an append‑only file per day
	// save to serverlogs directory, create directory if it doesn't exist
	if err := os.MkdirAll("serverlogs", 0755); err != nil {
		log.Println("cannot create serverlogs directory:", err)
		return
	}
	// create a file name based on the current date
	fileName := "serverlogs/" + time.Now().Format("2006-01-02") + ".webrtc.log"

    f, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        log.Println("cannot open log file:", err)
        return
    }
    defer f.Close()

    log.Printf("📝 log‑socket connected → %s", fileName)

    for {
        _, raw, err := conn.ReadMessage()
        if err != nil {
            log.Println("log‑socket closed:", err)
            return
        }
        // write to file
        if _, err := f.Write(append(raw, '\n')); err != nil {
            log.Println("file write error:", err)
        }
        // mirror to stdout (optional)
        log.Printf("[browser] %s", raw)
    }
}

