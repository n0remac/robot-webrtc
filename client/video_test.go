package client

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"
)

func TestCameraArgs(t *testing.T) {
	config := CameraConfig{Device: "/dev/video2", Resolution: "1280x720", FPS: 24}
	want := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-f", "v4l2",
		"-framerate", "24",
		"-video_size", "1280x720",
		"-i", "/dev/video2",
		"-vf", "hflip,vflip",
		"-an",
		"-c:v", "mjpeg",
		"-q:v", "5",
		"-f", "image2pipe",
		"pipe:1",
	}
	if got := cameraArgs(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("cameraArgs() = %q, want %q", got, want)
	}
}

func TestReadJPEG(t *testing.T) {
	want := []byte{0xff, 0xd8, 0x01, 0xff, 0x00, 0x02, 0xff, 0xd9}
	input := append([]byte("ignored"), want...)
	input = append(input, 0xff, 0xd8, 0x03, 0xff, 0xd9)
	got, err := readJPEG(bufio.NewReader(bytes.NewReader(input)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("readJPEG() = %v, want %v", got, want)
	}
}
