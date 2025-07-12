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

type ServoConfig struct {
	Min   float64
	Max   float64
	Angle float64
}

type server struct {
	UnimplementedControllerServer
	pca     *pca9685.ServoGroup
	moverMu sync.Mutex
	movers  map[int]chan struct{}
	servos  map[int]*ServoConfig
}

func NewServer(sg *pca9685.ServoGroup, servoRanges map[int][2]float64) *server {
	servos := make(map[int]*ServoConfig)
	for ch, rng := range servoRanges {
		mid := (rng[0] + rng[1]) / 2
		servos[ch] = &ServoConfig{
			Min:   rng[0],
			Max:   rng[1],
			Angle: mid,
		}
		// Set physical servo to mid
		servo := sg.GetServo(ch)
		servo.SetAngle(physic.Angle(mid))
	}
	return &server{
		pca:    sg,
		movers: make(map[int]chan struct{}),
		servos: servos,
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
	cfg, exists := s.servos[ch]
	if !exists {
		s.moverMu.Unlock()
		return &MoveReply{Ok: false, Err: "invalid servo channel"}, nil
	}
	if _, busy := s.movers[ch]; busy {
		s.moverMu.Unlock()
		return &MoveReply{Ok: false, Err: "already moving"}, nil
	}
	stop := make(chan struct{})
	s.movers[ch] = stop
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
				newAng := cfg.Angle + float64(dir)*speed*0.05
				if newAng > cfg.Max {
					newAng = cfg.Max
				}
				if newAng < cfg.Min {
					newAng = cfg.Min
				}
				cfg.Angle = newAng
				s.moverMu.Unlock()
				// set the hardware
				servo := s.pca.GetServo(ch)
				if err := servo.SetAngle(physic.Angle(newAng)); err != nil {
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

func (s *server) GetAngles(ctx context.Context, req *GetAnglesRequest) (*GetAnglesReply, error) {
	s.moverMu.Lock()
	defer s.moverMu.Unlock()
	var result []*ServoAngle
	for ch, cfg := range s.servos {
		result = append(result, &ServoAngle{
			Channel: int32(ch),
			Angle:   float32(cfg.Angle),
		})
	}
	return &GetAnglesReply{Angles: result}, nil
}
