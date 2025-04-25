package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// global state
var (
	peers      = make(map[string]*webrtc.PeerConnection)
	videoTrack *webrtc.TrackLocalStaticRTP
	audioTrack *webrtc.TrackLocalStaticRTP
)

func main() {
	// --- CLI flags ---
	// server := flag.String("server", "ws://localhost:8080/ws/hub", "signaling server URL")
	server := flag.String("server", "wss://noremac.dev/ws/hub", "signaling server URL")

	file := flag.String("file", "", "path to a local video file to stream instead of webcam")
	room := flag.String("room", "default", "room name")
	id := flag.String("id", "", "your unique client ID")
	flag.Parse()

	if *id == "" {
		*id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	myID := *id
	log.Printf("My ID: %s", myID)

	// decide which ffmpeg launcher to use:
	if *file != "" {
		// stream a file (at realtime) → two separate RTP streams
		go runFFmpegFileCLI(
			*file,
			"rtp://127.0.0.1:5004",
			map[string]string{
				"c:v":    "libx264",
				"preset": "ultrafast",
				"tune":   "zerolatency",
				"an":     "", // disable audio here
				"f":      "rtp",
			},
		)
		go runFFmpegFileCLI(
			*file,
			"rtp://127.0.0.1:5006",
			map[string]string{
				"c:a": "libopus",
				"vn":  "", // disable video here
				"f":   "rtp",
			},
		)
	} else {
		// your existing webcam + mic
		go runFFmpegCLI(
			"/dev/video0", "v4l2", 30, "640x480",
			"rtp://127.0.0.1:5004",
			map[string]string{"c:v": "libx264", "preset": "ultrafast", "tune": "zerolatency", "an": "", "f": "rtp"},
		)
		go runFFmpegCLI(
			"default", "alsa", 0, "",
			"rtp://127.0.0.1:5006",
			map[string]string{"c:a": "libopus", "vn": "", "f": "rtp"},
		)
	}

	// --- 2) Prepare static‐RTP tracks ---
	m := webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		log.Fatalf("RegisterDefaultCodecs: %v", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))

	var err error
	videoTrack, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: "video/H264"}, "video", "pion-video",
	)
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(video): %v", err)
	}
	audioTrack, err = webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion-audio",
	)
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(audio): %v", err)
	}

	// pump RTP into tracks
	go pumpRTP("[::]:5004", videoTrack)
	go pumpRTP("[::]:5006", audioTrack)

	// --- 3) Connect to signaling server ---
	wsURL := fmt.Sprintf("%s?room=%s", *server, *room)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatalf("WebSocket dial error: %v", err)
	}
	defer ws.Close()

	// send our join message
	ws.WriteJSON(map[string]interface{}{
		"join": myID, "from": myID, "room": *room,
	})

	// read incoming messages
	go func() {
		for {
			var msg map[string]interface{}
			if err := ws.ReadJSON(&msg); err != nil {
				log.Printf("WebSocket read error: %v", err)
				return
			}
			handleSignal(ws, api, myID, *room, msg)
		}
	}()

	// wait for CTRL+C
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Closing down")
}

func runFFmpegCLI(input, format string, fps int, size, output string, outArgs map[string]string) {
	// start with global flags
	args := []string{
		"-hide_banner",
		"-loglevel", "debug",
		"-f", format,
	}
	// video-specific options
	if fps > 0 {
		args = append(args,
			"-framerate", fmt.Sprint(fps),
			"-video_size", size,
		)
	}
	// specify input
	args = append(args, "-i", input)

	// append output options
	for flag, val := range outArgs {
		// ensure leading dash(s)
		f := flag
		if !strings.HasPrefix(f, "-") {
			f = "-" + f
		}
		args = append(args, f)
		if val != "" {
			args = append(args, val)
		}
	}

	// finally the output destination
	args = append(args, output)

	log.Printf("running ffmpeg %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}

// runFFmpegFileCLI streams a local file at realtime speed (-re) into a single RTP output URL.
func runFFmpegFileCLI(inputFile, output string, outArgs map[string]string) {
	// global + -re + input
	args := []string{
		"-hide_banner",
		"-loglevel", "debug",
		"-re", // read input “in realtime”
		"-i", inputFile,
	}
	// output flags
	for flag, val := range outArgs {
		f := flag
		if !strings.HasPrefix(f, "-") {
			f = "-" + f
		}
		args = append(args, f)
		if val != "" {
			args = append(args, val)
		}
	}
	args = append(args, output)

	log.Printf("running ffmpeg (file) %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}

// pumpRTP reads RTP packets from addr and writes them into track
func pumpRTP(addr string, track *webrtc.TrackLocalStaticRTP) {
	pcaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatalf("ResolveUDPAddr %s: %v", addr, err)
	}
	conn, err := net.ListenUDP("udp", pcaddr)
	if err != nil {
		log.Fatalf("ListenUDP %s: %v", addr, err)
	}
	defer conn.Close()

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		if err := track.WriteRTP(&pkt); err != nil {
			log.Printf("track.WriteRTP error: %v", err)
			return
		}
	}
}

// handleSignal processes join/offer/answer/candidate messages
func handleSignal(ws *websocket.Conn, api *webrtc.API, myID, room string, msg map[string]interface{}) {
	typ, _ := msg["type"].(string)
	from, _ := msg["from"].(string)
	to, _ := msg["to"].(string)

	// ignore messages not for us (except join)
	if typ != "join" && to != myID {
		return
	}
	// ignore our own join echoes
	if typ == "join" && from == myID {
		return
	}

	switch typ {
	case "join":
		// new peer joined → create PC + send offer
		log.Printf("Peer %s joined, creating PC and sending offer", from)
		pc := createPeerConnection(api, myID, from, room, ws)
		peers[from] = pc
		sendOffer(ws, pc, myID, from, room)

	case "offer":
		// incoming offer → create or reuse PC, set remote, then answer
		log.Printf("Received offer from %s", from)
		pc, ok := peers[from]
		if !ok {
			pc = createPeerConnection(api, myID, from, room, ws)
			peers[from] = pc
		}
		offer := msg["offer"].(map[string]interface{})
		sdp := offer["sdp"].(string)
		pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer, SDP: sdp,
		})
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Printf("CreateAnswer error: %v", err)
			return
		}
		pc.SetLocalDescription(answer)
		ws.WriteJSON(map[string]interface{}{
			"type":   "answer",
			"answer": pc.LocalDescription(),
			"from":   myID,
			"to":     from,
			"room":   room,
		})

	case "answer":
		// incoming answer → finish handshake
		log.Printf("Received answer from %s", from)
		answer := msg["answer"].(map[string]interface{})
		sdp := answer["sdp"].(string)
		peers[from].SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: sdp,
		})

	case "candidate":
		// incoming ICE → add to PC
		cand := msg["candidate"].(map[string]interface{})
		ice := webrtc.ICECandidateInit{
			Candidate:     cand["candidate"].(string),
			SDPMid:        ptrString(cand["sdpMid"].(string)),
			SDPMLineIndex: ptrUint16(uint16(cand["sdpMLineIndex"].(float64))),
		}
		if pc, ok := peers[from]; ok {
			pc.AddICECandidate(ice)
		}
	}
}

// createPeerConnection builds a new PC, adds tracks & ICE handler
func createPeerConnection(
	api *webrtc.API,
	myID, peerID, room string,
	ws *websocket.Conn,
) *webrtc.PeerConnection {
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		log.Fatalf("NewPeerConnection error: %v", err)
	}

	// add our media tracks
	if _, err = pc.AddTrack(videoTrack); err != nil {
		log.Fatalf("AddTrack video: %v", err)
	}
	if _, err = pc.AddTrack(audioTrack); err != nil {
		log.Fatalf("AddTrack audio: %v", err)
	}

	// when we generate ICE, send it to the peer
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		ice := c.ToJSON()
		ws.WriteJSON(map[string]interface{}{
			"type":      "candidate",
			"candidate": ice,
			"from":      myID,
			"to":        peerID,
			"room":      room,
		})
	})

	return pc
}

// sendOffer creates and sends an offer to the given peer
func sendOffer(ws *websocket.Conn, pc *webrtc.PeerConnection, myID, peerID, room string) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Printf("CreateOffer error: %v", err)
		return
	}
	pc.SetLocalDescription(offer)
	ws.WriteJSON(map[string]interface{}{
		"type":  "offer",
		"offer": pc.LocalDescription(),
		"from":  myID,
		"to":    peerID,
		"room":  room,
	})
}

// helpers for pointers
func ptrString(s string) *string { return &s }
func ptrUint16(u uint16) *uint16 { return &u }
