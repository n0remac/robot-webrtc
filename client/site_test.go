package client

import "testing"

func TestRobotWebSocketURL(t *testing.T) {
	tests := map[string]string{
		"http://localhost:8081":  "ws://localhost:8081/ws/robot",
		"https://makers.example": "wss://makers.example/ws/robot",
	}
	for input, want := range tests {
		got, err := robotWebSocketURL(input)
		if err != nil {
			t.Fatalf("robotWebSocketURL(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("robotWebSocketURL(%q) = %q, want %q", input, got, want)
		}
	}
}
