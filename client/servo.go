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
