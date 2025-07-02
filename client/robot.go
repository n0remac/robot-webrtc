package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stianeikeland/go-rpio/v4"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
	"periph.io/x/host/v3"
)

func SetupRobot() ([]*Motor, *pca9685.ServoGroup, func()) {
	// 1) Init Periph
	if _, err := host.Init(); err != nil {
		log.Fatal("host.Init:", err)
	}

	// 2) Open I²C bus #1
	bus, err := i2creg.Open("1")
	if err != nil {
		log.Fatal("i2creg.Open:", err)
	}

	// define a cleanup function that callers must invoke on shutdown
	cleanup := func() {
		bus.Close()
		rpio.Close()
	}

	// 3) Software reset the PCA9685 (General Call 0x06)
	if err := bus.Tx(0x00, []byte{0x06}, nil); err != nil {
		log.Println("PCA9685 SWRST failed:", err)
	}
	time.Sleep(10 * time.Millisecond)

	// 4) Create & configure the driver
	pca, err := pca9685.NewI2C(bus, pca9685.I2CAddr)
	if err != nil {
		log.Fatal("pca9685.NewI2C:", err)
	}
	if err := pca.SetPwmFreq(50 * physic.Hertz); err != nil {
		log.Fatal("SetPwmFreq:", err)
	}
	if err := pca.SetAllPwm(0, 0); err != nil {
		log.Fatal("SetAllPwm:", err)
	}
	servos := pca9685.NewServoGroup(pca, 50, 650, 0, 180)

	// Open GPIO memory
	if err := rpio.Open(); err != nil {
		fmt.Println("Unable to open GPIO:", err)
		return nil, nil, cleanup
	}

	// Create motors (these will use rpio.Pin under the hood)
	m1 := NewMotor("MOTOR1", 1)
	m2 := NewMotor("MOTOR2", 1)
	m3 := NewMotor("MOTOR3", 1)
	m4 := NewMotor("MOTOR4", 1)

	motors := []*Motor{m1, m2, m3, m4}

	return motors, servos, cleanup
}

func Controlls(motors []*Motor, servos *pca9685.ServoGroup) func(msg webrtc.DataChannelMessage) {
	return func(msg webrtc.DataChannelMessage) {
		m1 := motors[0]
		m2 := motors[1]
		m3 := motors[2]
		m4 := motors[3]

		log.Printf("Received message on DataChannel 'keyboard': %s", string(msg.Data))

		type Msg struct {
			Key    string
			Action string
		}
		var message Msg
		if err := json.Unmarshal(msg.Data, &message); err != nil {
			log.Printf("Error unmarshalling message: %v", err)
			return
		}

		log.Printf("Received action: %s", message.Action)
		log.Printf("Received key: %s", message.Key)

		const speed = 60 // degrees per second

		// helper to kick off or stop a move
		act := func(pin, dir int) {
			if message.Action == "pressed" {
				if err := Move(servos, pin, dir, speed); err != nil {
					log.Printf("Move error pin %d: %v", pin, err)
				}
			} else {
				Stop(pin)
			}
		}

		switch string(message.Key) {
		// Claw (pin 4): r=open, f=close
		case "r":
			act(4, +1)
		case "f":
			act(4, -1)

		// Up/Down (pin 6): t=up, g=down
		case "t":
			act(6, +1)
		case "g":
			act(6, -1)

		// Left/Right (pin 5): y=right, d=left
		case "y":
			act(5, +1)
		case "h":
			act(5, -1)

		// Camera tilt (pin 14): i=up, k=down
		case "i":
			act(14, +1)
		case "k":
			act(14, -1)

		// Camera pan (pin 15): l=right, j=left
		case "l":
			act(15, +1)
		case "j":
			act(15, -1)

		case "1":
			if message.Action == "pressed" {
				log.Println("1 key pressed")
				m1.Forward(100)
			} else if message.Action == "released" {
				log.Println("1 key released")
				m1.Stop()
			}
		case "2":
			if message.Action == "pressed" {
				log.Println("2 key pressed")
				m2.Forward(100)
			} else if message.Action == "released" {
				log.Println("2 key released")
				m2.Stop()
			}
		case "3":
			if message.Action == "pressed" {
				log.Println("3 key pressed")
				m3.Forward(100)
			} else if message.Action == "released" {
				log.Println("3 key released")
				m3.Stop()
			}
		case "4":
			if message.Action == "pressed" {
				log.Println("4 key pressed")
				m4.Forward(100)
			} else if message.Action == "released" {
				log.Println("4 key released")
				m4.Stop()
			}
		case "w":
			if message.Action == "pressed" {
				log.Println("w key pressed")
				m1.Reverse(100)
				m3.Forward(100)

				m2.Reverse(100)
				m4.Forward(100)
			} else if message.Action == "released" {
				log.Println("w key released")
				m1.Stop()
				m3.Stop()
				m2.Stop()
				m4.Stop()
			}
		case "s":
			log.Println("Backward command received")
			if message.Action == "pressed" {
				log.Println("s key pressed")
				m1.Forward(100)
				m3.Reverse(100)

				m2.Forward(100)
				m4.Reverse(100)
			} else if message.Action == "released" {
				log.Println("s key released")
				m1.Stop()
				m3.Stop()
				m2.Stop()
				m4.Stop()
			}
		case "a":
			log.Println("a key pressed")
			if message.Action == "pressed" {
				log.Println("a key pressed")
				m1.Forward(100)
				m3.Reverse(100)

				m2.Reverse(100)
				m4.Forward(100)
			} else if message.Action == "released" {
				log.Println("a key released")
				m1.Stop()
				m3.Stop()
				m2.Stop()
				m4.Stop()
			}
		case "d":
			log.Println("d key pressed")
			if message.Action == "pressed" {
				log.Println("d key pressed")
				m1.Reverse(100)
				m3.Forward(100)

				m2.Forward(100)
				m4.Reverse(100)

			} else if message.Action == "released" {
				log.Println("d key released")
				m1.Stop()
				m3.Stop()
				m2.Stop()
				m4.Stop()
			}
		}
	}
}

func runFFmpegCLI(input, format string, fps int, size, output string, outArgs map[string]string) {
	// start with global flags
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-f", format,
	}
	// video-specific options
	if fps > 0 {
		args = append(args,
			"-framerate", fmt.Sprint(fps),
			"-video_size", size,
		)
	}
	// specify input
	args = append(args, "-i", input)

	// append output options
	for flag, val := range outArgs {
		// ensure leading dash(s)
		f := flag
		if !strings.HasPrefix(f, "-") {
			f = "-" + f
		}
		args = append(args, f)
		if val != "" {
			args = append(args, val)
		}
	}

	// finally the output destination
	args = append(args, output)

	log.Printf("running ffmpeg %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}

// runFFmpegFileCLI streams a local file at realtime speed (-re) into a single RTP output URL.
func runFFmpegFileCLI(inputFile, output string, outArgs map[string]string) {
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
