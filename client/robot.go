package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	pb "github.com/n0remac/robot-webrtc/servo"
	"github.com/stianeikeland/go-rpio/v4"
)

type Motorer interface {
	Forward(speed float64)
	Reverse(speed float64)
	Stop()
	Test(bool)
}

type NopMotor struct{}

func (NopMotor) Forward(float64) {}
func (NopMotor) Reverse(float64) {}
func (NopMotor) Stop()           {}
func (NopMotor) Test(bool)       {}

func SetupRobot() []Motorer {
	// 0) Open the rpio driver — must do this *once* before any Pin.Output/Pin.Input calls
	if err := rpio.Open(); err != nil {
		log.Printf("⚠️  rpio.Open failed (%v); falling back to no-op motors", err)
		return []Motorer{NopMotor{}, NopMotor{}, NopMotor{}, NopMotor{}}
	}

	// Create motors (these will use rpio.Pin under the hood)
	m1 := NewMotor("MOTOR1", 1)
	m2 := NewMotor("MOTOR2", 1)
	m3 := NewMotor("MOTOR3", 1)
	m4 := NewMotor("MOTOR4", 1)

	return []Motorer{m1, m2, m3, m4}
}

// Controller translates browser control messages into motor and servo actions.
// It also tracks pressed keys so repeated browser keydown events are harmless.
type Controller struct {
	motors      []Motorer
	servoClient pb.ControllerClient
	mu          sync.Mutex
	pressed     map[string]bool
}

type ControlMessage struct {
	Type   string `json:"type,omitempty"`
	Key    string `json:"key,omitempty"`
	Action string `json:"action,omitempty"`
}

func NewController(motors []Motorer, servoClient pb.ControllerClient) *Controller {
	return &Controller{
		motors:      motors,
		servoClient: servoClient,
		pressed:     make(map[string]bool),
	}
}

// Handle applies one {key, action} JSON message. Heartbeats are accepted as a
// no-op; the WebSocket server uses them to enforce its dead-man timeout.
func (c *Controller) Handle(data []byte) error {
	const speed = 60 // degrees per second

	var m ControlMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("decode control message: %w", err)
	}
	if m.Type == "heartbeat" {
		return nil
	}
	if m.Action != "pressed" && m.Action != "released" {
		return fmt.Errorf("invalid action %q", m.Action)
	}
	if !validControlKey(m.Key) {
		return fmt.Errorf("invalid key %q", m.Key)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	wantPressed := m.Action == "pressed"
	if c.pressed[m.Key] == wantPressed {
		return nil
	}
	c.pressed[m.Key] = wantPressed

	// motors for numeric keys
	m1, m2, m3, m4 := c.motors[0], c.motors[1], c.motors[2], c.motors[3]

	// helper to call the servo RPC
	rpcAct := func(pin, dir int32) {
		if c.servoClient == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if m.Action == "pressed" {
			_, err := c.servoClient.Move(ctx, &pb.MoveRequest{
				Channel:   pin,
				Direction: dir,
				Speed:     speed,
			})
			if err != nil {
				log.Printf("Servo Move RPC error: %v", err)
			}
		} else {
			_, err := c.servoClient.Stop(ctx, &pb.StopRequest{
				Channel: pin,
			})
			if err != nil {
				log.Printf("Servo Stop RPC error: %v", err)
			}
		}
	}
	// 4 open claw, 5 turn claw, 6 lift claw, 14 pan camera, 15 tilt camera.
	switch m.Key {
	// Servos:
	case "y": // claw open
		rpcAct(4, +1)
	case "r": // claw close
		rpcAct(4, -1)
	case "t": // arm up
		rpcAct(6, +1)
	case "g": // arm down
		rpcAct(6, -1)
	case "f": // left/right
		rpcAct(5, +1)
	case "h":
		rpcAct(5, -1)
	case "i": // camera tilt
		rpcAct(15, +1)
	case "k":
		rpcAct(15, -1)
	case "l": // camera pan
		rpcAct(14, -1)
	case "j":
		rpcAct(14, +1)

	// Motors:
	case "w":
		if m.Action == "pressed" {
			m1.Reverse(100)
			m3.Forward(100)
			m2.Reverse(100)
			m4.Forward(100)
		} else {
			m1.Stop()
			m3.Stop()
			m2.Stop()
			m4.Stop()
		}
	case "s":
		if m.Action == "pressed" {
			m1.Forward(100)
			m3.Reverse(100)
			m2.Forward(100)
			m4.Reverse(100)
		} else {
			m1.Stop()
			m3.Stop()
			m2.Stop()
			m4.Stop()
		}
	case "a":
		if m.Action == "pressed" {
			m1.Forward(100)
			m3.Reverse(100)
			m2.Reverse(100)
			m4.Forward(100)
		} else {
			m1.Stop()
			m3.Stop()
			m2.Stop()
			m4.Stop()
		}
	case "d":
		if m.Action == "pressed" {
			m1.Reverse(100)
			m3.Forward(100)
			m2.Forward(100)
			m4.Reverse(100)
		} else {
			m1.Stop()
			m3.Stop()
			m2.Stop()
			m4.Stop()
		}
	}
	return nil
}

func validControlKey(key string) bool {
	switch key {
	case "w", "a", "s", "d", "t", "f", "g", "h", "i", "j", "k", "l", "r", "y":
		return true
	default:
		return false
	}
}

// StopAll is the safety stop used when the controller disconnects or becomes
// unresponsive. It is safe to call more than once.
func (c *Controller) StopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.pressed {
		c.pressed[key] = false
	}
	for _, motor := range c.motors {
		motor.Stop()
	}
	if c.servoClient == nil {
		return
	}
	for _, channel := range []int32{4, 5, 6, 14, 15} {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		_, err := c.servoClient.Stop(ctx, &pb.StopRequest{Channel: channel})
		cancel()
		if err != nil {
			log.Printf("servo %d safety stop: %v", channel, err)
		}
	}
}
