//go:build webrtc_legacy

package client

import (
	"log"

	pb "github.com/n0remac/robot-webrtc/servo"
	"github.com/pion/webrtc/v4"
)

// Controls adapts the direct controller to the former WebRTC data channel.
// It exists only to keep the opt-in legacy client buildable during migration.
func Controls(motors []Motorer, servoClient pb.ControllerClient) func(webrtc.DataChannelMessage) {
	controller := NewController(motors, servoClient)
	return func(message webrtc.DataChannelMessage) {
		if err := controller.Handle(message.Data); err != nil {
			log.Printf("ignored legacy control message: %v", err)
		}
	}
}
