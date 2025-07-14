package client

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	pb "github.com/n0remac/robot-webrtc/servo"
	"github.com/stianeikeland/go-rpio/v4"

	"github.com/pion/webrtc/v4"
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

func Controls(
	motors []Motorer,
	servoClient pb.ControllerClient,
) func(msg webrtc.DataChannelMessage) {
	const speed = 60 // degrees per second

	return func(msg webrtc.DataChannelMessage) {
		log.Printf("Received message on DataChannel 'keyboard': %s", string(msg.Data))
		type Msg struct {
			Key    string
			Action string
		}
		var m Msg
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			log.Printf("Error unmarshalling message: %v", err)
			return
		}
		log.Printf("Action=%s, Key=%q", m.Action, m.Key)

		// motors for numeric keys
		m1, m2, m3, m4 := motors[0], motors[1], motors[2], motors[3]

		// helper to call the servo RPC
		rpcAct := func(pin, dir int32) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if m.Action == "pressed" {
				_, err := servoClient.Move(ctx, &pb.MoveRequest{
					Channel:   pin,
					Direction: dir,
					Speed:     speed,
				})
				if err != nil {
					log.Printf("Servo Move RPC error: %v", err)
				}
			} else {
				_, err := servoClient.Stop(ctx, &pb.StopRequest{
					Channel: pin,
				})
				if err != nil {
					log.Printf("Servo Stop RPC error: %v", err)
				}
			}
		}
		// 4 open claw, 5 turn claw , 6 lift claw, 14, pan camera, 15 tilt camera
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
	}
}

func RunFFmpegCLI(args []string) {
	log.Printf("running ffmpeg %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}

// runFFmpegFileCLI streams a local file at realtime speed (-re) into a single RTP output URL.
func RunFFmpegFileCLI(inputFile, output string, outArgs map[string]string) {
	// global + -re + input
	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "warning",
		"-re", // read input “in realtime”
		"-i", inputFile,
	}
	// output flags
	for flag, val := range outArgs {
		f := flag
		if !strings.HasPrefix(f, "-") {
			f = "-" + f
		}
		args = append(args, f)
		if val != "" {
			args = append(args, val)
		}
	}
	args = append(args, output)

	log.Printf("running ffmpeg %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}
