package main

import (
	"flag"
	"log"
	"os"

	cl "github.com/n0remac/robot-webrtc/client"
)

var defaultSignalURL = "wss://orcasmaker.com/ws/webrtc"

func main() {
	signalURL := flag.String("signal-url", envOrDefault("ROBOT_WEBRTC_SIGNAL_URL", defaultSignalURL), "WebRTC signaling WebSocket URL")
	token := flag.String("robot-token", os.Getenv("ROBOT_WEBRTC_TOKEN"), "robot WebRTC bearer token")
	room := flag.String("room", "robot", "signaling room")
	flag.Parse()
	if *token == "" {
		log.Fatal("ROBOT_WEBRTC_TOKEN or -robot-token is required")
	}
	if err := cl.SetupWebRTC(*signalURL, *room, cl.SetupRobot(), "robot", *token); err != nil {
		log.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
