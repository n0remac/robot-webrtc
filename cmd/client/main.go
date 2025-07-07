// cmd/client/main.go
package main

import (
	"flag"
	"log"

	cl "github.com/n0remac/robot-webrtc/client"
)

func main() {
	motors := cl.SetupRobot()

	// CLI flags
	server := flag.String("server", "wss://noremac.dev/ws/hub", "signaling server URL")
	room := "robot"
	flag.Parse()

	myID := "robot"
	log.Printf("My ID: %s", myID)

	cl.Setup(server, &room, motors, myID)
}
