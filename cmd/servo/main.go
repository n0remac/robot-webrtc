// cmd/servo/main.go
package main

import (
	"log"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
	"periph.io/x/host/v3/sysfs" // only sysfs host drivers (i2c, led, thermal)

	pb "github.com/n0remac/robot-webrtc/servo"
)

type nopBus struct{}

var defaultServoRanges = map[int][2]float64{
	4:  {15, 140}, // Claw open/close
	5:  {15, 140}, // Claw rotation
	6:  {15, 140}, // Arm lift
	14: {15, 140}, // Camera pan
	15: {15, 140}, // Camera tilt
}

func (nopBus) Tx(addr uint16, w, r []byte) error  { return nil }
func (nopBus) Close() error                       { return nil }
func (nopBus) SetSpeed(hz physic.Frequency) error { return nil }
func (nopBus) String() string                     { return "nopBus" }

func main() {
	sg, cleanup := SetupServers()
	defer cleanup()

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterControllerServer(srv, pb.NewServer(sg, defaultServoRanges))
	log.Println("servo gRPC listening on :50051")
	srv.Serve(lis)
}

func SetupServers() (*pca9685.ServoGroup, func()) {
	// 1) Try opening the real /dev/i2c-1 bus:
	var bus i2c.BusCloser
	realBus, err := sysfs.NewI2C(1)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			log.Println("⚠️  /dev/i2c-1 not found, falling back to no-op I²C bus")
			bus = nopBus{}
		} else {
			log.Fatalf("sysfs.NewI2C: %v", err)
		}
	} else {
		bus = realBus
	}

	cleanup := func() { _ = bus.Close() }

	// 2) Software reset the PCA9685 (General Call 0x06)
	_ = bus.Tx(0x00, []byte{0x06}, nil)
	time.Sleep(10 * time.Millisecond)

	// 3) Create & configure the driver
	pca, err := pca9685.NewI2C(bus, pca9685.I2CAddr)
	if err != nil {
		log.Fatalf("pca9685.NewI2C: %v", err)
	}
	if err := pca.SetPwmFreq(50 * physic.Hertz); err != nil {
		log.Fatalf("SetPwmFreq: %v", err)
	}
	if err := pca.SetAllPwm(0, 0); err != nil {
		log.Fatalf("SetAllPwm: %v", err)
	}

	return pca9685.NewServoGroup(pca, 50, 650, 0, 180), cleanup
}
