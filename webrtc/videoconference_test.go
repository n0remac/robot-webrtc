package webrtc

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestTurnCredentialsRequireSecret(t *testing.T) {
	t.Setenv("TURN_SHARED_SECRET", "")
	t.Setenv("TURN_SHARED_SECRET_FILE", "")
	t.Setenv("TURN_PASS", "")

	recorder := httptest.NewRecorder()
	handleTurnCredentials(recorder, httptest.NewRequest(http.MethodGet, "/turn-credentials", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestTurnCredentialsMatchCoturnRESTAuthentication(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("TURN_SHARED_SECRET", secret)
	t.Setenv("TURN_SHARED_SECRET_FILE", "")
	t.Setenv("TURN_HOST", "turn.example.com")
	t.Setenv("TURN_PORT", "3478")
	t.Setenv("TURN_CREDENTIAL_TTL", "600")

	recorder := httptest.NewRecorder()
	handleTurnCredentials(recorder, httptest.NewRequest(http.MethodGet, "/turn-credentials?user=robot", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Username string   `json:"username"`
		Password string   `json:"password"`
		TTL      int64    `json:"ttl"`
		URLs     []string `json:"urls"`
		URIs     []string `json:"uris"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TTL != 600 || len(response.URLs) != 2 || len(response.URIs) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
	expiresText, user, found := strings.Cut(response.Username, ":")
	if !found || user != "robot" {
		t.Fatalf("username = %q", response.Username)
	}
	if _, err := strconv.ParseInt(expiresText, 10, 64); err != nil {
		t.Fatalf("username expiration = %q: %v", expiresText, err)
	}
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(response.Username))
	wantPassword := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if response.Password != wantPassword {
		t.Fatalf("password does not match Coturn REST HMAC")
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
}
