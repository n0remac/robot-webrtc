package client

import "testing"

func TestTurnCredentialsURL(t *testing.T) {
	tests := map[string]string{
		"wss://orcasmaker.com/ws/webrtc": "https://orcasmaker.com/webrtc/turn-credentials?user=robot",
		"ws://localhost:8081/ws/webrtc":  "http://localhost:8081/webrtc/turn-credentials?user=robot",
	}
	for input, want := range tests {
		got, err := turnCredentialsURL(input)
		if err != nil {
			t.Fatalf("turnCredentialsURL(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("turnCredentialsURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSignalParsersRejectMalformedPayloads(t *testing.T) {
	if _, ok := signalSDP(map[string]any{"sdp": ""}); ok {
		t.Fatal("empty SDP accepted")
	}
	if _, ok := signalCandidate(map[string]any{"candidate": 42}); ok {
		t.Fatal("non-string candidate accepted")
	}
}
