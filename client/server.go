package client

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pb "github.com/n0remac/robot-webrtc/servo"
)

const controlTimeout = time.Second

//go:embed web/*
var webAssets embed.FS

type Server struct {
	controller  *Controller
	servoClient pb.ControllerClient
	cameraURL   *url.URL
	upgrader    websocket.Upgrader
	controlTTL  time.Duration
	activeMu    sync.Mutex
	active      *websocket.Conn
}

func NewServer(controller *Controller, servoClient pb.ControllerClient, cameraURL string) (*Server, error) {
	target, err := url.Parse(cameraURL)
	if err != nil {
		return nil, fmt.Errorf("parse camera URL: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("camera URL must use http or https")
	}
	s := &Server{controller: controller, servoClient: servoClient, cameraURL: target, controlTTL: controlTimeout}
	s.upgrader.CheckOrigin = sameOrigin
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/control", s.handleControl)
	mux.HandleFunc("/api/servo-angles", s.handleServoAngles)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	cameraProxy := httputil.NewSingleHostReverseProxy(s.cameraURL)
	cameraProxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("camera proxy: %v", err)
		http.Error(w, "camera stream unavailable; is uStreamer running?", http.StatusServiceUnavailable)
	}
	mux.Handle("/stream", cameraProxy)

	static, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	return mux
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.activeMu.Lock()
	previous := s.active
	s.controller.StopAll()
	s.active = conn
	s.activeMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	log.Printf("controller connected from %s", r.RemoteAddr)
	defer func() {
		_ = conn.Close()
		s.activeMu.Lock()
		if s.active == conn {
			s.active = nil
			s.controller.StopAll()
		}
		s.activeMu.Unlock()
		log.Printf("controller disconnected from %s", r.RemoteAddr)
	}()

	_ = conn.SetReadDeadline(time.Now().Add(s.controlTTL))
	conn.SetReadLimit(1024)
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(s.controlTTL))
		if err := s.controller.Handle(data); err != nil {
			log.Printf("ignored control message: %v", err)
		}
	}
}

func (s *Server) handleServoAngles(w http.ResponseWriter, r *http.Request) {
	if s.servoClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "servo service unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	reply, err := s.servoClient.GetAngles(ctx, &pb.GetAnglesRequest{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reply.Angles)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
