package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	cl "robot-webrtc/client"
)

func main() {

	motors := cl.SetupRobot()

	// CLI flags
	server := flag.String("server", "wss://noremac.dev/ws/hub", "signaling server URL")
	// server := flag.String("server", "ws://localhost:8080/ws/hub", "signaling server URL")
	room := flag.String("room", "default", "room name")
	id := flag.String("id", "", "unique client ID")
	flag.Parse()

	// generate ID if none provided
	if *id == "" {
		*id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	myID := *id
	log.Printf("My ID: %s", myID)

	// fetch TURN credentials and build ICE servers
	serverBase := strings.TrimSuffix(strings.TrimPrefix(*server, "wss://"), "/ws/hub")
	creds, err := cl.FetchTurnCredentials("https://" + serverBase + "/turn-credentials")
	if err != nil {
		log.Printf("Warning: could not fetch TURN creds: %v", err)
	}
	cl.GlobalIceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	if creds != nil {
		for _, uri := range creds.URLs {
			cl.GlobalIceServers = append(cl.GlobalIceServers,
				webrtc.ICEServer{URLs: []string{uri}, Username: creds.Username, Credential: creds.Credential},
			)
		}
	}

	// prepare static-RTP tracks
	m := webrtc.MediaEngine{}
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f"},
		PayloadType:        109,
	}, webrtc.RTPCodecTypeVideo)
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))

	// create local RTP tracks
	cl.VideoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "video/H264"}, "video", "pion-video")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(video): %v", err)
	}
	cl.AudioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion-audio")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(audio): %v", err)
	}

	// pump RTP
	go cl.PumpRTP("[::]:5004", cl.VideoTrack, 109)
	go cl.PumpRTP("[::]:5006", cl.AudioTrack, 111)

	// handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// connect and maintain signalling
	go func() {
		for {
			if err := cl.ConnectAndSignal(api, myID, *room, *server, motors); err != nil {
				log.Printf("Signal loop exited with: %v; retrying in 1s...", err)
			}
			time.Sleep(time.Second)
		}
	}()

	// start FFmpeg push
	go cl.RunFFmpegCLI(
		"/dev/video0", "v4l2", 30, "640x480",
		"rtp://127.0.0.1:5004",
		map[string]string{
			"vf":           "hflip,vflip",
			"c:v":          "libx264",
			"preset":       "ultrafast",
			"tune":         "zerolatency",
			"pix_fmt":      "yuv420p",
			"an":           "",
			"f":            "rtp",
			"payload_type": "109",
		},
	)

	<-sigCh
	log.Println("Shutting down: sending leave & closing peers...")
	cl.PeersMu.Lock()
	for _, pc := range cl.Peers {
		pc.Close()
	}
	cl.PeersMu.Unlock()
}
