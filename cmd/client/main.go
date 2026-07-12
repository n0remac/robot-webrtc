// cmd/client/main.go
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	cl "github.com/n0remac/robot-webrtc/client"
	sv "github.com/n0remac/robot-webrtc/servo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	motors := cl.SetupRobot()

	addr := flag.String("addr", ":8080", "HTTP control server listen address")
	camera := flag.String("camera", "http://127.0.0.1:8081", "uStreamer base URL")
	servoTarget := flag.String("servo", "127.0.0.1:50051", "servo gRPC address")
	flag.Parse()

	conn, err := grpc.NewClient(*servoTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect to servo service: %v", err)
	}
	defer conn.Close()
	servoClient := sv.NewControllerClient(conn)
	controller := cl.NewController(motors, servoClient)
	defer controller.StopAll()

	server, err := cl.NewServer(controller, servoClient, *camera)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("robot controls: http://<robot-ip>%s", *addr)
	log.Printf("camera source: %s/stream", *camera)
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-signals:
		log.Printf("received %s; stopping robot", sig)
		controller.StopAll()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server: %v", err)
		}
	}
}
