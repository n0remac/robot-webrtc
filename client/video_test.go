package client

import (
	"reflect"
	"testing"
)

func TestCameraArgs(t *testing.T) {
	config := CameraConfig{Device: "/dev/video2", Resolution: "1280x720", FPS: 24, Port: 9000}
	want := []string{
		"--device=/dev/video2",
		"--resolution=1280x720",
		"--desired-fps=24",
		"--host=127.0.0.1",
		"--port=9000",
	}
	if got := cameraArgs(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("cameraArgs() = %q, want %q", got, want)
	}
}
