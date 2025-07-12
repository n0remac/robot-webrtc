package client

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
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

func Setup(server *string, room *string, motors []Motorer, myID string) {
	// fetch TURN credentials and build ICE servers
	serverBase := strings.TrimSuffix(strings.TrimPrefix(*server, "wss://"), "/ws/hub")
	creds, err := FetchTurnCredentials("https://" + serverBase + "/turn-credentials")
	if err != nil {
		log.Printf("Warning: could not fetch TURN creds: %v", err)
	}
	GlobalIceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	if creds != nil {
		for _, uri := range creds.URLs {
			GlobalIceServers = append(GlobalIceServers,
				webrtc.ICEServer{URLs: []string{uri}, Username: creds.Username, Credential: creds.Credential},
			)
		}
	}

	// prepare static-RTP tracks
	m := webrtc.MediaEngine{}
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    "video/H264",
			ClockRate:   90000,
			SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f",
		},
		PayloadType: 109,
	}, webrtc.RTPCodecTypeVideo)
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))

	// create local RTP tracks
	VideoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "video/H264"}, "video", "pion-video")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(video): %v", err)
	}
	AudioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion-audio")
	if err != nil {
		log.Fatalf("NewTrackLocalStaticRTP(audio): %v", err)
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
	defer conn.Close()

	if err != nil {
		log.Fatalf("failed to dial servo server: %v", err)
	}
	defer conn.Close()

	servoClient := sv.NewControllerClient(conn)

	// connect and maintain webRTC signalling
	go func() {
		for {
			if err := ConnectAndSignal(api, myID, *room, *server, motors, servoClient); err != nil {
				log.Printf("Signal loop exited with: %v; retrying in 1s...", err)
			}
			time.Sleep(time.Second)
		}
	}()

	// start FFmpeg push
	go HighStream.Start()
	go AudioStream.Start()

	<-sigCh
	log.Println("Shutting down: sending leave & closing peers...")
	PeersMu.Lock()
	for _, pc := range Peers {
		pc.Close()
	}
	PeersMu.Unlock()
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
	motors []Motorer,
	servoClient sv.ControllerClient,
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
		pc = createPeerConnection(api, myID, from, room, ws, motors, servoClient)
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
		raw := msg["offer"].(map[string]interface{})
		sdp := raw["sdp"].(string)
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

		rawAns := msg["answer"].(map[string]interface{})
		sdp := rawAns["sdp"].(string)
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
		raw := msg["candidate"].(map[string]interface{})
		ice := webrtc.ICECandidateInit{
			Candidate:     raw["candidate"].(string),
			SDPMid:        ptrString(raw["sdpMid"].(string)),
			SDPMLineIndex: ptrUint16(uint16(raw["sdpMLineIndex"].(float64))),
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
	motors []Motorer,
	servoClient sv.ControllerClient,
) *webrtc.PeerConnection {
	fmt.Println("Creating PeerConnection for", peerID)

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: GlobalIceServers,
	})
	if err != nil {
		log.Fatalf("NewPeerConnection error: %v", err)
	}

	dc, err := pc.CreateDataChannel("keyboard", nil)
	if err != nil {
		log.Printf("CreateDataChannel keyboard error: %v", err)
	} else {
		dc.OnOpen(func() {
			log.Printf("✔︎ Go DataChannel 'keyboard' open")
		})
		dc.OnMessage(Controls(motors, servoClient))
	}

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("▶︎ DataChannel '%s' from %s", dc.Label(), peerID)

		// optional: know when it’s open
		dc.OnOpen(func() {
			log.Printf("✔︎ DataChannel '%s' open", dc.Label())
		})

		// log every message
		pc.OnDataChannel(func(dc *webrtc.DataChannel) {
			log.Printf("▶︎ Incoming DataChannel '%s' from %s", dc.Label(), peerID)
			dc.OnOpen(func() {
				log.Printf("✔︎ Go DataChannel '%s' open", dc.Label())
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				log.Printf("← msg on '%s': %s", dc.Label(), string(msg.Data))
			})
		})

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
			restartICE(pc, ws, myID, peerID, room)
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
func ConnectAndSignal(api *webrtc.API, myID, room, wsURL string, motors []Motorer, servoClient sv.ControllerClient) error {
	// dial
	ws, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("%s?room=%s&playerId=robot", wsURL, room), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

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
		handleSignal(ws, api, myID, room, msg, motors, servoClient)
	}
}

// fetchTurnCredentials GETs the TURN credentials JSON
func FetchTurnCredentials(url string) (*turnCreds, error) {
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
