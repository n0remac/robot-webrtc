package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeMotor struct {
	mu      sync.Mutex
	forward int
	reverse int
	stop    int
}

func (m *fakeMotor) Forward(float64) { m.mu.Lock(); m.forward++; m.mu.Unlock() }
func (m *fakeMotor) Reverse(float64) { m.mu.Lock(); m.reverse++; m.mu.Unlock() }
func (m *fakeMotor) Stop()           { m.mu.Lock(); m.stop++; m.mu.Unlock() }
func (*fakeMotor) Test(bool)         {}

func (m *fakeMotor) counts() (forward, reverse, stop int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.forward, m.reverse, m.stop
}

func TestControllerIgnoresRepeatedKeydown(t *testing.T) {
	motors := []*fakeMotor{{}, {}, {}, {}}
	c := NewController(motorInterfaces(motors), nil)
	message := []byte(`{"key":"w","action":"pressed"}`)
	if err := c.Handle(message); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(message); err != nil {
		t.Fatal(err)
	}
	_, reverse, _ := motors[0].counts()
	if reverse != 1 {
		t.Fatalf("motor activated %d times, want 1", reverse)
	}
}

func TestControlSocketStopsRobotAfterHeartbeatTimeout(t *testing.T) {
	camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "frame")
	}))
	defer camera.Close()

	motors := []*fakeMotor{{}, {}, {}, {}}
	c := NewController(motorInterfaces(motors), nil)
	s, err := NewServer(c, nil, camera.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.controlTTL = 75 * time.Millisecond
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/control"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(ControlMessage{Key: "w", Action: "pressed"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	_, _, stops := motors[0].counts()
	if stops < 2 { // one on connect, one after the dead-man timeout
		t.Fatalf("motor stop count = %d, want at least 2", stops)
	}
}

func TestServerServesPageAndProxiesCamera(t *testing.T) {
	camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			t.Errorf("camera path = %q, want /stream", r.URL.Path)
		}
		_, _ = io.WriteString(w, "camera-data")
	}))
	defer camera.Close()

	motors := []*fakeMotor{{}, {}, {}, {}}
	s, err := NewServer(NewController(motorInterfaces(motors), nil), nil, camera.URL)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()

	for path, contains := range map[string]string{"/": "Robot Control", "/stream": "camera-data", "/health": `"status":"ok"`} {
		response, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), contains) {
			t.Errorf("GET %s: status=%d body=%q", path, response.StatusCode, body)
		}
	}
}

func motorInterfaces(motors []*fakeMotor) []Motorer {
	result := make([]Motorer, len(motors))
	for i, motor := range motors {
		result[i] = motor
	}
	return result
}
