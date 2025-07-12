package client

import (
	"os"
	"os/exec"
)

type StreamProcess struct {
	Cmd  *exec.Cmd
	Args []string
}

func (sp *StreamProcess) Start() error {
	sp.Cmd = exec.Command("ffmpeg", sp.Args...)
	sp.Cmd.Stdout = os.Stdout
	sp.Cmd.Stderr = os.Stderr
	return sp.Cmd.Start()
}

func (sp *StreamProcess) Stop() error {
	if sp.Cmd != nil && sp.Cmd.Process != nil {
		return sp.Cmd.Process.Kill()
	}
	return nil
}

var (
	VideoArgsHigh = []string{
		"-hide_banner", "-loglevel", "warning",
		"-f", "v4l2", "-framerate", "30", "-video_size", "640x480", "-i", "/dev/video0",
		"-vf", "hflip,vflip",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-an",
		"-f", "rtp", "-payload_type", "109", "rtp://127.0.0.1:5004",
	}
	VideoArgsLow = []string{
		"-hide_banner", "-loglevel", "warning",
		"-f", "v4l2", "-framerate", "10", "-video_size", "320x240", "-i", "/dev/video0",
		"-vf", "hflip,vflip",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-b:v", "150k", "-pix_fmt", "yuv420p", "-an",
		"-f", "rtp", "-payload_type", "119", "rtp://127.0.0.1:5014",
	}

	AudioArgs = []string{
		"-hide_banner", "-loglevel", "warning",
		"-f", "alsa",
		"-ar", "48000",
		"-ac", "1",
		"-i", "hw:1,0",
		"-acodec", "libopus",
		"-f", "rtp",
		"-payload_type", "111",
		"rtp://127.0.0.1:5006",
	}
)

var (
	HighStream = &StreamProcess{Args: VideoArgsHigh}
	LowStream  = &StreamProcess{Args: VideoArgsLow}
	AudioStream = &StreamProcess{Args: AudioArgs}
)

func SwitchToHigh() {
	LowStream.Stop()
	HighStream.Start()
}

func SwitchToLow() {
	HighStream.Stop()
	LowStream.Start()
}
