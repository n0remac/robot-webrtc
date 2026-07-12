package servo

import (
	"context"

	"google.golang.org/grpc"
)

type localClient struct {
	service ControllerServer
}

// NewLocalClient exposes the servo service through the generated client
// interface without starting a local TCP/gRPC server.
func NewLocalClient(service ControllerServer) ControllerClient {
	return &localClient{service: service}
}

func (c *localClient) Move(ctx context.Context, request *MoveRequest, _ ...grpc.CallOption) (*MoveReply, error) {
	return c.service.Move(ctx, request)
}

func (c *localClient) Stop(ctx context.Context, request *StopRequest, _ ...grpc.CallOption) (*StopReply, error) {
	return c.service.Stop(ctx, request)
}

func (c *localClient) GetAngles(ctx context.Context, request *GetAnglesRequest, _ ...grpc.CallOption) (*GetAnglesReply, error) {
	return c.service.GetAngles(ctx, request)
}
