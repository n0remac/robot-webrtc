package servo

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
	"periph.io/x/host/v3/sysfs"
)

type nopBus struct{}

func (nopBus) Tx(uint16, []byte, []byte) error { return nil }
func (nopBus) Close() error                    { return nil }
func (nopBus) SetSpeed(physic.Frequency) error { return nil }
func (nopBus) String() string                  { return "nopBus" }

// DefaultRanges returns the configured movement limits for the robot's servos.
func DefaultRanges() map[int][2]float64 {
	return map[int][2]float64{
		4:  {15, 140}, // Claw open/close
		5:  {15, 140}, // Claw rotation
		6:  {15, 68},  // Arm lift
		14: {15, 140}, // Camera pan
		15: {15, 140}, // Camera tilt
	}
}

// SetupHardware opens and initializes the robot's PCA9685 servo controller.
// On a development machine without /dev/i2c-1 it uses a no-op bus, matching the
// motor package's existing non-Raspberry-Pi behavior.
func SetupHardware() (*pca9685.ServoGroup, func(), error) {
	var bus i2c.BusCloser
	realBus, err := sysfs.NewI2C(1)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			log.Printf("/dev/i2c-1 not found; using no-op servo bus")
			bus = nopBus{}
		} else {
			return nil, nil, fmt.Errorf("open /dev/i2c-1: %w", err)
		}
	} else {
		bus = realBus
	}

	cleanup := func() { _ = bus.Close() }
	if err := bus.Tx(0x00, []byte{0x06}, nil); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("reset PCA9685: %w", err)
	}
	time.Sleep(10 * time.Millisecond)

	pca, err := pca9685.NewI2C(bus, pca9685.I2CAddr)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("initialize PCA9685: %w", err)
	}
	if err := pca.SetPwmFreq(50 * physic.Hertz); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("set PCA9685 frequency: %w", err)
	}
	if err := pca.SetAllPwm(0, 0); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("clear PCA9685 outputs: %w", err)
	}

	return pca9685.NewServoGroup(pca, 50, 650, 0, 180), cleanup, nil
}
