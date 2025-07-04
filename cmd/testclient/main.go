// testclient.go
package main

import (
	"context"
	"flag"
	"log"
	"time"

	pb "github.com/n0remac/robot-webrtc/servo"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// CLI flags
	target := flag.String("target", "localhost:50051", "gRPC servo server address")
	pin := flag.Int("pin", -1, "servo channel (0–15)")
	direction := flag.Int("direction", 1, "1 = forward, -1 = reverse (ignored with -stop)")
	speed := flag.Float64("speed", 60, "degrees per second (ignored with -stop)")
	duration := flag.Duration("duration", 0, "how long to move before stopping (e.g. 2s; 0 = no auto-stop)")
	stopOnly := flag.Bool("stop", false, "if true, only send Stop for the given pin and exit")
	flag.Parse()

	if *pin < 0 {
		log.Fatalf("Must specify a valid -pin (0–15)")
	}

	// 1) Dial the servo server once
	cc, err := grpc.NewClient(
		*target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("Failed to create gRPC client for servo server at %s: %v", *target, err)
	}
	defer cc.Close()

	client := pb.NewControllerClient(cc)

	// 2) If -stop is set, just send Stop and exit
	if *stopOnly {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, err := client.Stop(ctx, &pb.StopRequest{Channel: int32(*pin)})
		if err != nil {
			log.Fatalf("Stop RPC failed: %v", err)
		}
		log.Printf("Stop.ok = %v", resp.GetOk())
		return
	}

	// 3) Otherwise, send Move
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := client.Move(ctx, &pb.MoveRequest{
		Channel:   int32(*pin),
		Direction: int32(*direction),
		Speed:     *speed,
	})
	if err != nil {
		log.Fatalf("Move RPC failed: %v", err)
	}
	log.Printf("Move.ok = %v", resp.GetOk())

	// 4) If a duration was specified, wait then Stop
	if *duration > 0 {
		log.Printf("Running for %v…", *duration)
		time.Sleep(*duration)
		ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
		defer cancel2()
		sresp, serr := client.Stop(ctx2, &pb.StopRequest{Channel: int32(*pin)})
		if serr != nil {
			log.Fatalf("Stop RPC failed: %v", serr)
		}
		log.Printf("Auto-Stop.ok = %v", sresp.GetOk())
	}
}
