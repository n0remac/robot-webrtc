package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const maxJPEGSize = 16 << 20

type CameraConfig struct {
	Binary     string
	Device     string
	Resolution string
	FPS        int
}

// CameraProcess owns FFmpeg and serves the most recently captured JPEG frame
// as an MJPEG HTTP stream.
type CameraProcess struct {
	cancel context.CancelFunc
	done   chan error
	mu     sync.RWMutex
	frame  []byte
	seq    uint64
}

func (p *CameraProcess) Done() <-chan error { return p.done }

// LatestFrame returns a copy of the newest JPEG and its sequence number.
// Callers can use the sequence number to avoid sending the same frame twice.
func (p *CameraProcess) LatestFrame() ([]byte, uint64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]byte(nil), p.frame...), p.seq
}

func (p *CameraProcess) Stop() {
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		log.Printf("timed out waiting for FFmpeg to stop")
	}
}

// StartCamera launches FFmpeg and reads an image2pipe MJPEG stream from stdout.
func StartCamera(parent context.Context, config CameraConfig) (*CameraProcess, error) {
	if config.Binary == "" {
		config.Binary = "ffmpeg"
	}
	if config.Device == "" || config.Resolution == "" || config.FPS <= 0 {
		return nil, fmt.Errorf("invalid camera configuration")
	}
	binary, err := exec.LookPath(config.Binary)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", config.Binary, err)
	}

	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, binary, cameraArgs(config)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open FFmpeg output: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start FFmpeg: %w", err)
	}

	process := &CameraProcess{cancel: cancel, done: make(chan error, 1)}
	go process.capture(ctx, cmd, stdout)
	log.Printf("FFmpeg camera started (pid %d, device %s)", cmd.Process.Pid, config.Device)
	return process, nil
}

func (p *CameraProcess) capture(ctx context.Context, cmd *exec.Cmd, stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 256*1024)
	var captureErr error
	for {
		frame, err := readJPEG(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				captureErr = fmt.Errorf("read FFmpeg frame: %w", err)
			}
			break
		}
		p.mu.Lock()
		p.frame = frame
		p.seq++
		p.mu.Unlock()
	}
	if captureErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		p.done <- nil
	} else if captureErr != nil {
		p.done <- captureErr
	} else if waitErr != nil {
		p.done <- fmt.Errorf("FFmpeg exited: %w", waitErr)
	} else {
		p.done <- errors.New("FFmpeg exited unexpectedly")
	}
	close(p.done)
}

func (p *CameraProcess) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Connection", "close")

	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	var sent uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			p.mu.RLock()
			seq := p.seq
			frame := append([]byte(nil), p.frame...)
			p.mu.RUnlock()
			if seq == 0 || seq == sent {
				continue
			}
			if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\r\n"); err != nil {
				return
			}
			flusher.Flush()
			sent = seq
		}
	}
}

func cameraArgs(config CameraConfig) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-f", "v4l2",
		"-framerate", strconv.Itoa(config.FPS),
		"-video_size", config.Resolution,
		"-i", config.Device,
		"-vf", "hflip,vflip",
		"-an",
		"-c:v", "mjpeg",
		"-q:v", "5",
		"-f", "image2pipe",
		"pipe:1",
	}
}

func readJPEG(reader *bufio.Reader) ([]byte, error) {
	previous := byte(0)
	for {
		current, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		if previous == 0xff && current == 0xd8 {
			break
		}
		previous = current
	}

	frame := make([]byte, 0, 256*1024)
	frame = append(frame, 0xff, 0xd8)
	previous = 0xd8
	for {
		current, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		frame = append(frame, current)
		if previous == 0xff && current == 0xd9 {
			return frame, nil
		}
		if len(frame) > maxJPEGSize {
			return nil, fmt.Errorf("JPEG exceeds %d bytes", maxJPEGSize)
		}
		previous = current
	}
}
