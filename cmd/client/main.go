// cmd/client/main.go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	cl "github.com/n0remac/robot-webrtc/client"
	sv "github.com/n0remac/robot-webrtc/servo"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "HTTP control server listen address")
	videoBinary := flag.String("video-binary", "ustreamer", "uStreamer executable")
	videoDevice := flag.String("video-device", "/dev/video0", "camera device")
	videoResolution := flag.String("video-resolution", "640x480", "camera resolution")
	videoFPS := flag.Int("video-fps", 30, "camera frames per second")
	videoPort := flag.Int("video-port", 8081, "loopback camera streamer port")
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
		Port:       *videoPort,
	})
	if err != nil {
		return err
	}
	defer camera.Stop()

	cameraURL := fmt.Sprintf("http://127.0.0.1:%d", *videoPort)
	server, err := cl.NewServer(controller, servoClient, cameraURL)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	httpErrors := make(chan error, 1)
	go func() { httpErrors <- httpServer.ListenAndServe() }()

	log.Printf("robot controls: http://<robot-ip>%s", *addr)
	log.Printf("motors, servos, and camera are running in one robot process")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	var runErr error
	select {
	case sig := <-signals:
		log.Printf("received %s; stopping robot", sig)
	case err := <-httpErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("HTTP server: %w", err)
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("HTTP shutdown: %w", err)
	}
	return runErr
}
