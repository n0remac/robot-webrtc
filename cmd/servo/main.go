// cmd/servo/main.go
package main

import (
	"log"
	"net"

	"google.golang.org/grpc"

	pb "github.com/n0remac/robot-webrtc/servo"
)

func main() {
	sg, cleanup, err := pb.SetupHardware()
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterControllerServer(srv, pb.NewServer(sg, pb.DefaultRanges()))
	log.Println("servo gRPC listening on :50051")
	if err := srv.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
