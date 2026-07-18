package client

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sv "github.com/n0remac/robot-webrtc/servo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var wsWriteMu sync.Mutex

// TURN credentials struct
type turnCreds struct {
	Username   string   `json:"username"`
	Credential string   `json:"password"`
	URLs       []string `json:"uris"`
}

// global state
var (
	PeersMu          sync.Mutex
	Peers            = make(map[string]*webrtc.PeerConnection)
	makingOfferMu    sync.Mutex
	makingOffer      = make(map[string]bool)
	queuedCandsMu    sync.Mutex
	queuedCandidates = make(map[string][]webrtc.ICECandidateInit)
	GlobalIceServers []webrtc.ICEServer
	VideoTrack       *webrtc.TrackLocalStaticRTP
	AudioTrack       *webrtc.TrackLocalStaticRTP
)

func SetupWebRTC(server, room string, motors []Motorer, myID, token string) error {
	// fetch TURN credentials and build ICE servers
	credentialsURL, err := turnCredentialsURL(server)
	if err != nil {
		return err
	}
	creds, err := FetchTurnCredentials(credentialsURL, token)
	if err != nil {
		return fmt.Errorf("fetch TURN credentials: %w", err)
	}
	GlobalIceServers = turnICEServers(creds)

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
	VideoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "video/H264"}, "video", "pion-video")
	if err != nil {
		return fmt.Errorf("create video track: %w", err)
	}
	AudioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion-audio")
	if err != nil {
		return fmt.Errorf("create audio track: %w", err)
	}

	// pump RTP
	go PumpRTP("[::]:5004", VideoTrack, 109)
	go PumpRTP("[::]:5006", AudioTrack, 111)

	// handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// connect to servo server
	target := "127.0.0.1:50051"
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial servo server: %w", err)
	}
	defer conn.Close()

	servoClient := sv.NewControllerClient(conn)
	controller := NewController(motors, servoClient)
	defer controller.StopAll()

	// connect and maintain webRTC signalling
	go func() {
		backoff := time.Second
		for {
			if refreshed, refreshErr := FetchTurnCredentials(credentialsURL, token); refreshErr == nil {
				GlobalIceServers = turnICEServers(refreshed)
			} else {
				log.Printf("refresh TURN credentials: %v", refreshErr)
			}
			if err := ConnectAndSignal(api, myID, room, server, token, controller); err != nil {
				controller.StopAll()
				log.Printf("signal loop exited: %v; retrying in %s", err, backoff)
			}
			time.Sleep(backoff)
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}()

	// start FFmpeg push
	go RunFFmpegCLI(
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
	PeersMu.Lock()
	for _, pc := range Peers {
		pc.Close()
	}
	PeersMu.Unlock()
	controller.StopAll()
	return nil
}

// pumpRTP reads RTP packets from addr and writes them into track
func PumpRTP(addr string, track *webrtc.TrackLocalStaticRTP, payloadType uint8) {
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
	controller *Controller,
) {
	typ, _ := msg["type"].(string)
	from, _ := msg["from"].(string)
	to, _ := msg["to"].(string)

	// allow only join or messages addressed to us
	if typ != "join" && to != myID {
		return
	}
	// drop our own join echo
	if typ == "join" && from == myID {
		return
	}

	// get-or-create (with mutex)
	getOrCreatePC := func() *webrtc.PeerConnection {
		PeersMu.Lock()
		pc := Peers[from]
		PeersMu.Unlock()
		if pc != nil {
			return pc
		}
		pc = createPeerConnection(api, myID, from, room, ws, controller)
		PeersMu.Lock()
		Peers[from] = pc
		PeersMu.Unlock()
		return pc
	}

	fmt.Println("Handling signal type:", typ)

	switch typ {
	case "join":
		log.Printf("Peer %s joined → creating PC + DC offer", from)
		_ = getOrCreatePC()
	case "offer":
		log.Printf("Received offer from %s", from)
		pc := getOrCreatePC()

		// 1) only answer when stable
		if pc.SignalingState() != webrtc.SignalingStateStable {
			log.Printf("  → dropping offer; state=%s", pc.SignalingState())
			return
		}

		// 2) set remote
		sdp, ok := signalSDP(msg["offer"])
		if !ok {
			log.Printf("invalid offer from %s", from)
			return
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer, SDP: sdp,
		}); err != nil {
			log.Printf("  → SetRemoteDescription error: %v", err)
			return
		}

		// 3) flush queued ICE (under lock)
		queuedCandsMu.Lock()
		for _, cand := range queuedCandidates[from] {
			if err := pc.AddICECandidate(cand); err != nil {
				log.Printf("  → queued AddICECandidate error: %v", err)
			}
		}
		queuedCandidates[from] = nil
		queuedCandsMu.Unlock()

		// 4) answer
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			log.Printf("  → CreateAnswer error: %v", err)
			return
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			log.Printf("  → SetLocalDescription(answer) error: %v", err)
			return
		}

		// 5) send it
		wsWriteMu.Lock()
		ws.WriteJSON(map[string]interface{}{
			"type":   "answer",
			"answer": pc.LocalDescription(),
			"from":   myID,
			"to":     from,
			"room":   room,
			"name":   "robot",
		})
		wsWriteMu.Unlock()

	case "answer":
		log.Printf("Received answer from %s", from)
		PeersMu.Lock()
		pc := Peers[from]
		PeersMu.Unlock()
		if pc == nil {
			log.Printf("No PC found for %s on answer", from)
			return
		}

		sdp, ok := signalSDP(msg["answer"])
		if !ok {
			log.Printf("invalid answer from %s", from)
			return
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: sdp,
		}); err != nil {
			log.Printf("SetRemoteDescription(answer) error: %v", err)
		}

		// flush queued ICE for answers too
		queuedCandsMu.Lock()
		for _, cand := range queuedCandidates[from] {
			if err := pc.AddICECandidate(cand); err != nil {
				log.Printf("Queued AddICECandidate error: %v", err)
			}
		}
		queuedCandidates[from] = nil
		queuedCandsMu.Unlock()

	case "candidate":
		fmt.Printf("Received ICE candidate from %s\n", from)
		ice, ok := signalCandidate(msg["candidate"])
		if !ok {
			log.Printf("invalid ICE candidate from %s", from)
			return
		}

		PeersMu.Lock()
		pc := Peers[from]
		PeersMu.Unlock()
		// buffer or add
		queuedCandsMu.Lock()
		if pc == nil || pc.RemoteDescription() == nil {
			queuedCandidates[from] = append(queuedCandidates[from], ice)
		} else if err := pc.AddICECandidate(ice); err != nil {
			log.Printf("AddICECandidate error: %v", err)
		}
		queuedCandsMu.Unlock()

	case "leave":
		log.Printf("Peer %s left → cleaning up", from)
		PeersMu.Lock()
		pc := Peers[from]
		delete(Peers, from)
		PeersMu.Unlock()
		if pc != nil {
			pc.Close()
		}
		controller.StopAll()
		makingOfferMu.Lock()
		delete(makingOffer, from)
		makingOfferMu.Unlock()
		queuedCandsMu.Lock()
		delete(queuedCandidates, from)
		queuedCandsMu.Unlock()
	}
}

func createPeerConnection(
	api *webrtc.API,
	myID, peerID, room string,
	ws *websocket.Conn,
	controller *Controller,
) *webrtc.PeerConnection {
	fmt.Println("Creating PeerConnection for", peerID)

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: GlobalIceServers,
	})
	if err != nil {
		log.Fatalf("NewPeerConnection error: %v", err)
	}
	var lastControl atomic.Int64
	var controlsActive atomic.Bool
	watchdogDone := make(chan struct{})
	var stopWatchdog sync.Once
	touchControls := func() {
		lastControl.Store(time.Now().UnixNano())
		controlsActive.Store(true)
	}
	handleControl := func(msg webrtc.DataChannelMessage) {
		touchControls()
		if err := controller.Handle(msg.Data); err != nil {
			log.Printf("ignored control message: %v", err)
		}
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-ticker.C:
				last := lastControl.Load()
				if controlsActive.Load() && last > 0 && time.Since(time.Unix(0, last)) > time.Second {
					controlsActive.Store(false)
					controller.StopAll()
					log.Printf("control heartbeat timed out; stopped robot")
				}
			}
		}
	}()

	dc, err := pc.CreateDataChannel("keyboard", nil)
	if err != nil {
		log.Printf("CreateDataChannel keyboard error: %v", err)
	} else {
		dc.OnOpen(func() {
			log.Printf("✔︎ Go DataChannel 'keyboard' open")
			touchControls()
		})
		dc.OnMessage(handleControl)
		dc.OnClose(controller.StopAll)
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("▶︎ DataChannel '%s' from %s", dc.Label(), peerID)

		dc.OnOpen(func() {
			log.Printf("✔︎ DataChannel '%s' open", dc.Label())
			touchControls()
		})
		dc.OnMessage(handleControl)
		dc.OnClose(controller.StopAll)
	})

	pc.OnNegotiationNeeded(func() {
		fmt.Println("OnNegotiationNeeded for", peerID)

		if pc.SignalingState() != webrtc.SignalingStateStable {
			log.Printf("ICE-restart: PC not stable for %s, skipping restart", peerID)
			return
		}

		makingOfferMu.Lock()
		makingOffer[peerID] = true
		makingOfferMu.Unlock()

		fmt.Println("Creating offer for", peerID)
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			log.Printf("OnNegotiationNeeded CreateOffer: %v", err)
			return
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			log.Printf("OnNegotiationNeeded SetLocalDescription: %v", err)
			return
		}
		wsWriteMu.Lock()

		fmt.Println("Sending offer to", peerID)
		ws.WriteJSON(map[string]interface{}{
			"type":  "offer",
			"offer": pc.LocalDescription(),
			"from":  myID,
			"to":    peerID,
			"room":  room,
			"name":  "robot",
		})
		wsWriteMu.Unlock()

		// clear the flag once sent
		makingOfferMu.Lock()
		makingOffer[peerID] = false
		makingOfferMu.Unlock()
		fmt.Println("▶ Offer sent to", peerID)
	})

	// register ICE-candidate and connection handlers
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		wsWriteMu.Lock()
		ws.WriteJSON(map[string]interface{}{
			"type":      "candidate",
			"candidate": c.ToJSON(),
			"from":      myID,
			"to":        peerID,
			"room":      room,
			"name":      "robot",
		})
		wsWriteMu.Unlock()
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateFailed {
			controller.StopAll()
			restartICE(pc, ws, myID, peerID, room)
		}
		if s == webrtc.ICEConnectionStateDisconnected || s == webrtc.ICEConnectionStateClosed {
			controller.StopAll()
		}
		if s == webrtc.ICEConnectionStateClosed {
			stopWatchdog.Do(func() { close(watchdogDone) })
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected || s == webrtc.PeerConnectionStateClosed {
			controlsActive.Store(false)
			controller.StopAll()
		}
		if s == webrtc.PeerConnectionStateClosed {
			stopWatchdog.Do(func() { close(watchdogDone) })
		}
	})

	// add tracks _after_ OnNegotiationNeeded is set
	if _, err := pc.AddTrack(VideoTrack); err != nil {
		log.Fatalf("AddTrack video: %v", err)
	}
	if _, err := pc.AddTrack(AudioTrack); err != nil {
		log.Fatalf("AddTrack audio: %v", err)
	}

	return pc
}

// helpers for pointers
func ptrString(s string) *string { return &s }
func ptrUint16(u uint16) *uint16 { return &u }

func restartICE(pc *webrtc.PeerConnection, ws *websocket.Conn, myID, peerID, room string) {
	fmt.Println("Restarting ICE for", peerID)

	if pc.SignalingState() != webrtc.SignalingStateStable {
		log.Printf("ICE-restart: PC not stable for %s, skipping restart", peerID)
		return
	}

	makingOffer[peerID] = true
	offer, err := pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		log.Println("ICE-restart CreateOffer:", err)
		return
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Println("ICE-restart SetLocalDesc:", err)
		return
	}
	wsWriteMu.Lock()
	ws.WriteJSON(map[string]interface{}{
		"type":  "offer",
		"offer": pc.LocalDescription(),
		"from":  myID,
		"to":    peerID,
		"room":  room,
		"name":  "robot",
	})
	wsWriteMu.Unlock()
	log.Printf("▶ ICE-restart sent to %s", peerID)
	makingOffer[peerID] = false
}

// connectAndSignal manages WebSocket signalling (with auto-reconnect)
func ConnectAndSignal(api *webrtc.API, myID, room, wsURL, token string, controller *Controller) error {
	// dial
	endpoint, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("parse signaling URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("room", room)
	query.Set("playerId", robotID)
	endpoint.RawQuery = query.Encode()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	ws, _, err := websocket.DefaultDialer.Dial(endpoint.String(), header)
	if err != nil {
		return err
	}
	defer func() {
		_ = ws.Close()
		closeAllPeers(controller)
	}()

	// send join
	wsWriteMu.Lock()
	ws.WriteJSON(map[string]interface{}{"type": "join", "join": myID, "from": myID, "room": room, "name": "robot"})
	wsWriteMu.Unlock()
	// read loop
	for {
		var msg map[string]interface{}
		if err := ws.ReadJSON(&msg); err != nil {
			return err
		}
		handleSignal(ws, api, myID, room, msg, controller)
	}
}

func turnICEServers(credentials *turnCreds) []webrtc.ICEServer {
	if credentials == nil {
		return nil
	}
	servers := make([]webrtc.ICEServer, 0, len(credentials.URLs))
	for _, uri := range credentials.URLs {
		servers = append(servers, webrtc.ICEServer{URLs: []string{uri}, Username: credentials.Username, Credential: credentials.Credential})
	}
	return servers
}

func closeAllPeers(controller *Controller) {
	PeersMu.Lock()
	peers := Peers
	Peers = make(map[string]*webrtc.PeerConnection)
	PeersMu.Unlock()
	for _, pc := range peers {
		_ = pc.Close()
	}
	queuedCandsMu.Lock()
	queuedCandidates = make(map[string][]webrtc.ICECandidateInit)
	queuedCandsMu.Unlock()
	makingOfferMu.Lock()
	makingOffer = make(map[string]bool)
	makingOfferMu.Unlock()
	controller.StopAll()
}

// fetchTurnCredentials GETs the TURN credentials JSON
func FetchTurnCredentials(endpoint, token string) (*turnCreds, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TURN endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var creds turnCreds
	if err := json.Unmarshal(body, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

const robotID = "robot"

func turnCredentialsURL(signalingURL string) (string, error) {
	u, err := url.Parse(signalingURL)
	if err != nil {
		return "", fmt.Errorf("parse signaling URL: %w", err)
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("signaling URL must use ws or wss")
	}
	u.Path = "/webrtc/turn-credentials"
	u.RawQuery = "user=robot"
	u.Fragment = ""
	return u.String(), nil
}

func signalSDP(value any) (string, bool) {
	raw, ok := value.(map[string]interface{})
	if !ok {
		return "", false
	}
	sdp, ok := raw["sdp"].(string)
	return sdp, ok && sdp != "" && len(sdp) <= 1<<20
}

func signalCandidate(value any) (webrtc.ICECandidateInit, bool) {
	raw, ok := value.(map[string]interface{})
	if !ok {
		return webrtc.ICECandidateInit{}, false
	}
	candidate, ok := raw["candidate"].(string)
	if !ok || candidate == "" || len(candidate) > 64<<10 {
		return webrtc.ICECandidateInit{}, false
	}
	result := webrtc.ICECandidateInit{Candidate: candidate}
	if mid, ok := raw["sdpMid"].(string); ok {
		result.SDPMid = ptrString(mid)
	}
	if line, ok := raw["sdpMLineIndex"].(float64); ok && line >= 0 && line <= 65535 {
		result.SDPMLineIndex = ptrUint16(uint16(line))
	}
	return result, true
}

func RunFFmpegCLI(input, format string, fps int, size, output string, outArgs map[string]string) {
	args := []string{"-hide_banner", "-loglevel", "warning", "-f", format}
	if fps > 0 {
		args = append(args, "-framerate", fmt.Sprint(fps), "-video_size", size)
	}
	args = append(args, "-i", input)
	for flag, value := range outArgs {
		if !strings.HasPrefix(flag, "-") {
			flag = "-" + flag
		}
		args = append(args, flag)
		if value != "" {
			args = append(args, value)
		}
	}
	args = append(args, output)
	log.Printf("running ffmpeg %v", args)
	command := exec.Command("ffmpeg", args...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		log.Printf("ffmpeg stopped: %v", err)
	}
}
