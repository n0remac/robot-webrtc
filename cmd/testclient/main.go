package main

import (
	"context"
	"log"
	"time"

	pb "github.com/n0remac/robot-webrtc/servo"
	"google.golang.org/grpc"
)

func main() {
	conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewControllerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Move channel 4
	r, err := c.Move(ctx, &pb.MoveRequest{Channel: 4, Direction: 1, Speed: 60})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Move.ok = %v", r.Ok)

	time.Sleep(2 * time.Second)

	// Stop channel 4
	s, err := c.Stop(ctx, &pb.StopRequest{Channel: 4})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Stop.ok = %v", s.Ok)
}
