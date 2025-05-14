package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TURN credentials struct
type turnCreds struct {
	Username   string   `json:"username"`
	Credential string   `json:"password"`
	URLs       []string `json:"uris"`
}

// global state
var (
	peersMu          sync.Mutex
	peers            = make(map[string]*webrtc.PeerConnection)
	makingOfferMu    sync.Mutex
	makingOffer      = make(map[string]bool)
	queuedCandsMu    sync.Mutex
	queuedCandidates = make(map[string][]webrtc.ICECandidateInit)
	globalIceServers []webrtc.ICEServer
	videoTrack       *webrtc.TrackLocalStaticRTP
	audioTrack       *webrtc.TrackLocalStaticRTP
)

func main() {
	// CLI flags
	server := flag.String("server", "wss://noremac.dev/ws/hub", "signaling server URL")
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
	creds, err := fetchTurnCredentials("https://" + serverBase + "/turn-credentials")
	if err != nil {
		log.Printf("Warning: could not fetch TURN creds: %v", err)
	}
	globalIceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	if creds != nil {
		for _, uri := range creds.URLs {
			globalIceServers = append(globalIceServers,
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
	videoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "video/H264"}, "video", "pion-video")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(video): %v", err)
	}
	audioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion-audio")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(audio): %v", err)
	}

	// handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// connect and maintain signalling
	go func() {
		for {
			if err := connectAndSignal(api, myID, *room, *server); err != nil {
				log.Printf("Signal loop exited with: %v; retrying in 1s...", err)
			}
			time.Sleep(time.Second)
		}
	}()

	// start FFmpeg push
	go runFFmpegCLI(
		"/dev/video0", "v4l2", 30, "640x480",
		"rtp://127.0.0.1:5004",
		map[string]string{"c:v": "libx264", "preset": "ultrafast", "tune": "zerolatency", "pix_fmt": "yuv420p", "an": "", "f": "rtp", "payload_type": "109"},
	)

	<-sigCh
	log.Println("Shutting down: sending leave & closing peers...")
	peersMu.Lock()
	for _, pc := range peers {
		pc.Close()
	}
	peersMu.Unlock()
}

func runFFmpegCLI(input, format string, fps int, size, output string, outArgs map[string]string) {
	// start with global flags
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
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
		"-y",
		"-hide_banner",
		"-loglevel", "warning",
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

	log.Printf("running ffmpeg %v", args)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("ffmpeg failed: %v", err)
	}
}

// pumpRTP reads RTP packets from addr and writes them into track
func pumpRTP(addr string, track *webrtc.TrackLocalStaticRTP, payloadType uint8) {
	log.Printf("▶ pumpRTP listening on %s (payload %d) → track %s", addr, payloadType, track.ID())
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatalf("ResolveUDPAddr %s: %v", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("ListenUDP %s: %v", addr, err)
	}
	defer conn.Close()

	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("pumpRTP(%s) read error: %v", addr, err)
			return
		}

		// log.Printf("📦 pumpRTP(%s) got %d bytes from %s", addr, n, src)
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			log.Printf("pumpRTP(%s) unmarshal error: %v", addr, err)
			continue
		}

		// force the correct PT
		pkt.Header.PayloadType = payloadType

		// retry until SRTP is ready
		for {
			if err := track.WriteRTP(&pkt); err != nil {
				log.Printf("pumpRTP(%s) WriteRTP error (will retry): %v", addr, err)
				time.Sleep(50 * time.Millisecond)
				continue
			}
			break
		}
	}
}

// handleSignal processes join/offer/answer/candidate/leave messages
func handleSignal(
	ws *websocket.Conn,
	api *webrtc.API,
	myID, room string,
	msg map[string]interface{},
) {
	typ, _ := msg["type"].(string)
	from, _ := msg["from"].(string)
	to, _ := msg["to"].(string)

	// always allow join messages, but drop everything else not addressed to us
	if typ != "join" && to != myID {
		return
	}
	// ignore our own join echos
	if typ == "join" && from == myID {
		return
	}

	// helper to get-or-create PC
	getOrCreatePC := func() *webrtc.PeerConnection {
		if pc := peers[from]; pc != nil {
			return pc
		}
		pc := createPeerConnection(api, myID, from, room, ws)
		peers[from] = pc
		return pc
	}

	// polite-peer: lower lexical ID wins
	polite := myID < from

	switch typ {
	case "join":
		log.Printf("Peer %s joined → creating PC & sending offer", from)
		pc := getOrCreatePC()
		// mark that we're about to make an offer
		makingOffer[from] = true
		sendOffer(ws, pc, myID, from, room)
	case "offer":
		log.Printf("Received offer from %s", from)
		pc := getOrCreatePC()

		collision := makingOffer[from] || pc.SignalingState() != webrtc.SignalingStateStable
		ignoreOffer := !polite && collision
		if ignoreOffer {
			log.Printf("Ignoring offer from %s (collision)", from)
			return
		}
		if collision {
			// rollback our unfinished local description
			if err := pc.SetLocalDescription(
				webrtc.SessionDescription{Type: webrtc.SDPTypeRollback},
			); err != nil {
				log.Printf("Rollback error: %v", err)
			}
		}

		// apply remote offer
		rawOffer := msg["offer"].(map[string]interface{})
		sdp := rawOffer["sdp"].(string)
		if err := pc.SetRemoteDescription(
			webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp},
		); err != nil {
			log.Printf("SetRemoteDescription error: %v", err)
			return
		}

		// flush any queued ICE candidates
		for _, cand := range queuedCandidates[from] {
			if err := pc.AddICECandidate(cand); err != nil {
				log.Printf("Queued AddICECandidate error: %v", err)
			}
		}
		queuedCandidates[from] = nil

		// answer
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Printf("CreateAnswer error: %v", err)
			return
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			log.Printf("SetLocalDescription(answer) error: %v", err)
			return
		}
		// answered, so no longer making an offer
		makingOffer[from] = false

		ws.WriteJSON(map[string]interface{}{
			"type":   "answer",
			"answer": pc.LocalDescription(),
			"from":   myID,
			"to":     from,
			"room":   room,
			"name":   "robot",
		})
	case "answer":
		log.Printf("Received answer from %s", from)
		pc := peers[from]
		if pc == nil {
			log.Printf("No PC found for %s on answer", from)
			return
		}
		rawAns := msg["answer"].(map[string]interface{})
		sdp := rawAns["sdp"].(string)
		if err := pc.SetRemoteDescription(
			webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp},
		); err != nil {
			log.Printf("SetRemoteDescription(answer) error: %v", err)
		}
		// flush any queued ICE candidates
		for _, cand := range queuedCandidates[from] {
			if err := pc.AddICECandidate(cand); err != nil {
				log.Printf("Queued AddICECandidate error: %v", err)
			}
		}
		queuedCandidates[from] = nil
	case "candidate":
		raw := msg["candidate"].(map[string]interface{})
		ice := webrtc.ICECandidateInit{
			Candidate:     raw["candidate"].(string),
			SDPMid:        ptrString(raw["sdpMid"].(string)),
			SDPMLineIndex: ptrUint16(uint16(raw["sdpMLineIndex"].(float64))),
		}

		pc := peers[from]
		// if remote not set yet, buffer it
		if pc == nil || pc.RemoteDescription() == nil {
			queuedCandidates[from] = append(queuedCandidates[from], ice)
		} else {
			if err := pc.AddICECandidate(ice); err != nil {
				log.Printf("AddICECandidate error: %v", err)
			}
		}
	case "leave":
		log.Printf("Peer %s left → cleaning up", from)
		if pc := peers[from]; pc != nil {
			pc.Close()
			delete(peers, from)
			delete(makingOffer, from)
			delete(queuedCandidates, from)
		}
	default:
		// unknown message types are ignored
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

	// pump RTP into tracks
	go pumpRTP("[::]:5004", videoTrack, 109)
	go pumpRTP("[::]:5006", audioTrack, 111)

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

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("🏓 PeerConnection state with %s: %s", peerID, s.String())
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateFailed {
			restartICE(pc, ws, myID, peerID, room)
		}
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

func restartICE(pc *webrtc.PeerConnection, ws *websocket.Conn, myID, peerID, room string) {
	offer, err := pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		log.Println("ICE-restart CreateOffer:", err)
		return
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Println("ICE-restart SetLocalDesc:", err)
		return
	}
	ws.WriteJSON(map[string]interface{}{
		"type":  "offer",
		"offer": pc.LocalDescription(),
		"from":  myID,
		"to":    peerID,
		"room":  room,
	})
	log.Printf("▶ ICE-restart sent to %s", peerID)
}

// connectAndSignal manages WebSocket signalling (with auto-reconnect)
func connectAndSignal(api *webrtc.API, myID, room, wsURL string) error {
	// dial
	ws, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("%s?room=%s", wsURL, room), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	// send join
	ws.WriteJSON(map[string]interface{}{"type": "join", "join": myID, "from": myID, "room": room})

	// read loop
	for {
		var msg map[string]interface{}
		if err := ws.ReadJSON(&msg); err != nil {
			return err
		}
		handleSignal(ws, api, myID, room, msg)
	}
}

// fetchTurnCredentials GETs the TURN credentials JSON
func fetchTurnCredentials(url string) (*turnCreds, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TURN endpoint returned %d", resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var creds turnCreds
	if err := json.Unmarshal(body, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}
