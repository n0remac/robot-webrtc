// cmd/client/main.go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	cl "github.com/n0remac/robot-webrtc/client"
	sv "github.com/n0remac/robot-webrtc/servo"
)

// defaultSiteURL is localhost for development. Production builds set it with:
// -ldflags "-X main.defaultSiteURL=https://example.com"
var defaultSiteURL = "http://localhost:8081"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	siteURL := flag.String("site-url", envOrDefault("ROBOT_SITE_URL", defaultSiteURL), "website URL")
	robotToken := flag.String("robot-token", os.Getenv("ROBOT_TOKEN"), "website robot authentication token")
	videoBinary := flag.String("video-binary", "ffmpeg", "FFmpeg executable")
	videoDevice := flag.String("video-device", "/dev/video0", "camera device")
	videoResolution := flag.String("video-resolution", "640x480", "camera resolution")
	videoFPS := flag.Int("video-fps", 30, "camera frames per second")
	flag.Parse()

	motors := cl.SetupRobot()
	servoGroup, closeServos, err := sv.SetupHardware()
	if err != nil {
		return err
	}
	defer closeServos()

	servoService := sv.NewServer(servoGroup, sv.DefaultRanges())
	servoClient := sv.NewLocalClient(servoService)
	log.Printf("servo control: in-process PCA9685 service (no gRPC listener required)")
	controller := cl.NewController(motors, servoClient)
	defer controller.StopAll()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	camera, err := cl.StartCamera(ctx, cl.CameraConfig{
		Binary:     *videoBinary,
		Device:     *videoDevice,
		Resolution: *videoResolution,
		FPS:        *videoFPS,
	})
	if err != nil {
		return err
	}
	defer camera.Stop()

	siteClient := &cl.SiteClient{URL: *siteURL, Token: *robotToken, Controller: controller, Camera: camera}
	siteErrors := make(chan error, 1)
	go func() { siteErrors <- siteClient.Run(ctx) }()

	log.Printf("robot controls are hosted by %s", *siteURL)
	log.Printf("motors, servos, camera, and website connection are running in one robot process")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	var runErr error
	select {
	case sig := <-signals:
		log.Printf("received %s; stopping robot", sig)
	case err := <-siteErrors:
		if err != nil {
			runErr = fmt.Errorf("website client: %w", err)
		}
	case err := <-camera.Done():
		if err == nil {
			runErr = errors.New("camera streamer stopped unexpectedly")
		} else {
			runErr = fmt.Errorf("camera streamer stopped: %w", err)
		}
	}

	controller.StopAll()
	cancel()
	return runErr
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
