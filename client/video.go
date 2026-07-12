package client

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type CameraConfig struct {
	Binary     string
	Device     string
	Resolution string
	FPS        int
	Port       int
}

type CameraProcess struct {
	cancel context.CancelFunc
	done   chan error
}

func (p *CameraProcess) Done() <-chan error { return p.done }

func (p *CameraProcess) Stop() {
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		log.Printf("timed out waiting for camera streamer to stop")
	}
}

// StartCamera launches uStreamer as a child of the robot process. Its HTTP
// listener is loopback-only and is exposed to the browser through /stream.
func StartCamera(parent context.Context, config CameraConfig) (*CameraProcess, error) {
	if config.Binary == "" {
		config.Binary = "ustreamer"
	}
	if config.Device == "" || config.Resolution == "" || config.FPS <= 0 || config.Port <= 0 || config.Port > 65535 {
		return nil, fmt.Errorf("invalid camera configuration")
	}
	binary, err := exec.LookPath(config.Binary)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", config.Binary, err)
	}

	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, binary, cameraArgs(config)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start camera streamer: %w", err)
	}

	process := &CameraProcess{cancel: cancel, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		if ctx.Err() == nil {
			process.done <- err
		}
		close(process.done)
	}()
	log.Printf("camera streamer started (pid %d, device %s)", cmd.Process.Pid, config.Device)
	return process, nil
}

func cameraArgs(config CameraConfig) []string {
	return []string{
		"--device=" + config.Device,
		"--resolution=" + config.Resolution,
		"--desired-fps=" + strconv.Itoa(config.FPS),
		"--host=127.0.0.1",
		"--port=" + strconv.Itoa(config.Port),
	}
}
