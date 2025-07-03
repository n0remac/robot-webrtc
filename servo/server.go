package servo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"log"

	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
)

type server struct {
	UnimplementedControllerServer
	pca     *pca9685.ServoGroup
	moverMu sync.Mutex
	movers  map[int]chan struct{}
	angles  map[int]float64
}

func NewServer(sg *pca9685.ServoGroup) *server {
	return &server{
		pca:    sg,
		movers: make(map[int]chan struct{}),
		angles: make(map[int]float64),
	}
}

func (s *server) Move(ctx context.Context, req *MoveRequest) (*MoveReply, error) {
	fmt.Printf("Move request: %+v\n", req)
	ch := int(req.Channel)
	dir := int(req.Direction)
	speed := float64(req.Speed)

	if dir != 1 && dir != -1 {
		return &MoveReply{Ok: false, Err: "direction must be +1 or -1"}, nil
	}

	s.moverMu.Lock()
	if _, busy := s.movers[ch]; busy {
		s.moverMu.Unlock()
		return &MoveReply{Ok: false, Err: "already moving"}, nil
	}
	stop := make(chan struct{})
	s.movers[ch] = stop
	if _, ok := s.angles[ch]; !ok {
		s.angles[ch] = 90
	}
	s.moverMu.Unlock()

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.moverMu.Lock()
				ang := s.angles[ch] + float64(dir)*speed*0.05
				if ang > 180 {
					ang = 180
				}
				if ang < 0 {
					ang = 0
				}
				s.angles[ch] = ang
				s.moverMu.Unlock()

				// set the hardware
				servo := s.pca.GetServo(ch)
				if err := servo.SetAngle(physic.Angle(ang)); err != nil {
					log.Printf("servo %d set angle error: %v", ch, err)
				}
			}
		}
	}()

	return &MoveReply{Ok: true}, nil
}

func (s *server) Stop(ctx context.Context, req *StopRequest) (*StopReply, error) {
	ch := int(req.Channel)
	s.moverMu.Lock()
	if c, ok := s.movers[ch]; ok {
		close(c)
		delete(s.movers, ch)
	}
	s.moverMu.Unlock()
	return &StopReply{Ok: true}, nil
}
