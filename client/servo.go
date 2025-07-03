package client

import (
	"fmt"
	"log"
	"sync"
	"time"

	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
)

// global state for movers
var (
	moverMu       sync.Mutex
	moverChans    = make(map[int]chan struct{})
	currentAngles = make(map[int]float64)
	i2cMu         sync.Mutex
)

func Move(servos *pca9685.ServoGroup, pin int, direction int, speed float64) error {
	if direction != 1 && direction != -1 {
		return fmt.Errorf("direction must be +1 or -1")
	}

	moverMu.Lock()
	if _, busy := moverChans[pin]; busy {
		moverMu.Unlock()
		return fmt.Errorf("servo %d already moving", pin)
	}
	stop := make(chan struct{})
	moverChans[pin] = stop

	// initialize to 90° if we've never set it yet
	if _, ok := currentAngles[pin]; !ok {
		currentAngles[pin] = 90
	}
	moverMu.Unlock()

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// compute next angle
				moverMu.Lock()
				ang := currentAngles[pin] + float64(direction)*speed*(0.05) // 0.05s per tick
				if ang > 180 {
					ang = 180
				} else if ang < 0 {
					ang = 0
				}
				currentAngles[pin] = ang
				moverMu.Unlock()

				// apply it
				if err := setServo(servos, pin, ang); err != nil {
					log.Printf("move servo %d error: %v", pin, err)
				}
			}
		}
	}()

	return nil
}

// Stop stops any in-flight Move on that pin.
func Stop(pin int) {
	moverMu.Lock()
	defer moverMu.Unlock()
	if ch, ok := moverChans[pin]; ok {
		close(ch)
		delete(moverChans, pin)
	}
}

func setServo(servos *pca9685.ServoGroup, pin int, angle float64) error {
	i2cMu.Lock()
	defer i2cMu.Unlock()
	servo := servos.GetServo(pin)
	return servo.SetAngle(physic.Angle(angle))
}


// func main() {
// 	// 1) Initialize host drivers
// 	if _, err := host.Init(); err != nil {
// 		log.Fatal("host.Init:", err)
// 	}

// 	// 2) Open the I²C bus
// 	bus, err := i2creg.Open("")
// 	if err != nil {
// 		log.Fatal("i2creg.Open:", err)
// 	}
// 	defer bus.Close()

// 	// 3) Create PCA9685 and set it up for 50 Hz
// 	pca, err := pca9685.NewI2C(bus, pca9685.I2CAddr)
// 	if err != nil {
// 		log.Fatal("pca9685.NewI2C:", err)
// 	}
// 	if err := pca.SetPwmFreq(50 * physic.Hertz); err != nil {
// 		log.Fatal("SetPwmFreq:", err)
// 	}
// 	if err := pca.SetAllPwm(0, 0); err != nil {
// 		log.Fatal("SetAllPwm:", err)
// 	}

// 	// 4) Build a ServoGroup (0°–180° from tick 50→650)
// 	servos := pca9685.NewServoGroup(pca, 50, 650, 0, 180)

// 	// 5) Demo: move pin 4 forward at 60°/s
// 	pin := 4
// 	log.Println("▶ Moving servo pin forward at 60°/s")
// 	if err := Move(servos, pin, +1, 60); err != nil {
// 		log.Fatal(err)
// 	}
// 	time.Sleep(2 * time.Second)
// 	Stop(pin)
// 	log.Println("⏹ Stopped servo pin")

// 	// 6) Demo: move pin 6 backward at 30°/s
// 	log.Println("▶ Moving servo 6 backward at 30°/s")
// 	if err := Move(servos, 6, -1, 30); err != nil {
// 		log.Fatal(err)
// 	}
// 	time.Sleep(3 * time.Second)
// 	Stop(6)
// 	log.Println("⏹ Stopped servo 6")

// 	log.Println("✅ Demo complete")
// }

// Move starts a background loop that every 50ms steps the given servo
// (channel) forward (direction=+1) or backward (direction=-1) at
// speed degrees per second.  It returns immediately; call Stop(pin)
// to break the loop.