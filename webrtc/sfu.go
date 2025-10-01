package webrtc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	cvpipe "github.com/n0remac/robot-webrtc/cvpipe"
	wsock "github.com/n0remac/robot-webrtc/websocket"
	"github.com/pion/interceptor" // <-- Add this import
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

/* -------------------------- Types & Wire Messages -------------------------- */

type sfuMessage struct {
	Type      string                     `json:"type"`
	Name      string                     `json:"name,omitempty"`
	From      string                     `json:"from,omitempty"`
	Room      string                     `json:"room,omitempty"`
	Offer     *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer    *webrtc.SessionDescription `json:"answer,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

/* --------------------------------- SFU Core -------------------------------- */

type sfuPeer struct {
	id   string
	room string

	conn *websocket.Conn
	send chan []byte // single writer goroutine, hub-style

	pc *webrtc.PeerConnection

	// For each publisherID and trackID, keep sender for cleanup/removal
	sendersMu sync.Mutex
	// key: pubID|trackID -> sender
	senders map[string]*webrtc.RTPSender

	// (optional) quick maps for direct write-by-pub
	localVideo map[string]*webrtc.TrackLocalStaticRTP // key: pubID|trackID
	localAudio map[string]*webrtc.TrackLocalStaticRTP // key: pubID|trackID

	// candidates buffered until RemoteDescription set
	candMu    sync.Mutex
	candQueue []webrtc.ICECandidateInit
	remoteSet bool

	// negotiation coalescing
	negCh   chan struct{}
	negOnce sync.Once

	// ICE restart guard
	restartMu    sync.Mutex
	iceRestartIn bool

	closed chan struct{}

	procMu    sync.Mutex
	procPipes map[string]*cvpipe.Pipeline

	makingOffer atomic.Bool
	polite      bool

	keyframeGate map[string]*keyGate
	keyframeMu   sync.Mutex
}

type keyGate struct {
	waiting bool
	lastPLI time.Time
}

type pubTrack struct {
	remote  *webrtc.TrackRemote
	kind    webrtc.RTPCodecType
	pubID   string
	trackID string
	pubPC   *webrtc.PeerConnection
}

type sfuRoom struct {
	mu     sync.Mutex
	peers  map[string]*sfuPeer
	roomID string

	// publisherID -> trackID -> pubTrack
	pubs map[string]map[string]*pubTrack
}

type sfuServer struct {
	mu    sync.Mutex
	rooms map[string]*sfuRoom
	api   *webrtc.API
}

var sfu = &sfuServer{
	rooms: make(map[string]*sfuRoom),
	api:   newSFUAPI(),
}

/* ----------------------------- Pion API / codecs ---------------------------- */

func newSFUAPI() *webrtc.API {
	m := &webrtc.MediaEngine{}
	// Opus:
	_ = m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000, Channels: 2,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio)
	// H264 (same as above)
	_ = m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "goog-remb"}},
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo)
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		panic(err)
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))
}

// For now, let server do public STUN only; keep symmetric to the client.
var sfuIceServers = []webrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
}

/* --------------------------------- Routing --------------------------------- */

func (s *sfuServer) getRoom(id string) *sfuRoom {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm, ok := s.rooms[id]
	if !ok {
		rm = &sfuRoom{
			peers:  make(map[string]*sfuPeer),
			pubs:   make(map[string]map[string]*pubTrack),
			roomID: id,
		}
		s.rooms[id] = rm
	}

	return rm
}

func (r *sfuRoom) addPeer(p *sfuPeer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[p.id] = p
}

func (r *sfuRoom) delPeer(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peers, id)
}

func (r *sfuRoom) others(except string) []*sfuPeer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*sfuPeer, 0, len(r.peers))
	for id, p := range r.peers {
		if id == except {
			continue
		}
		out = append(out, p)
	}
	return out
}

/* --------------------------------- Handler --------------------------------- */

// SfuWebsocketHandler wires /ws/sfu?room=...&id=... to a durable per-peer WS with a Pion PC.
func SfuWebsocketHandler(w http.ResponseWriter, r *http.Request) {
	room := r.URL.Query().Get("room")
	if room == "" {
		room = "default"
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		id = randomSFUID()
	}

	// Reuse your Upgrader (origin check, buffer sizes) & WS durability patterns
	conn, err := wsock.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[SFU] WS upgrade failed: %v", err)
		return
	}
	log.Printf("[SFU] WS connected room=%s id=%s", room, id)

	pc, err := sfu.api.NewPeerConnection(webrtc.Configuration{ICEServers: sfuIceServers})
	if err != nil {
		_ = conn.Close()
		log.Printf("[SFU] PeerConnection create error: %v", err)
		return
	}
	_, _ = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	_, _ = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})

	p := &sfuPeer{
		id:           id,
		room:         room,
		conn:         conn,
		send:         make(chan []byte, 256), // bounded like your hub
		pc:           pc,
		senders:      make(map[string]*webrtc.RTPSender),
		localVideo:   make(map[string]*webrtc.TrackLocalStaticRTP),
		localAudio:   make(map[string]*webrtc.TrackLocalStaticRTP),
		negCh:        make(chan struct{}, 1),
		closed:       make(chan struct{}),
		procPipes:    make(map[string]*cvpipe.Pipeline),
		polite:       false,
		keyframeGate: make(map[string]*keyGate),
	}

	p.negOnce.Do(func() { go negotiatorWorker(p) })

	rm := sfu.getRoom(room)
	rm.addPeer(p)

	// Wire Pion events
	wirePeerEvents(p, rm)

	// Start writer (single goroutine) before we may send anything
	go writePumpSFU(p)

	rm.attachRawFanoutToSubscriber(p)

	// Start reader (messages → signaling)
	readPumpSFU(p, rm)

	rm.mu.Lock()
	deadTracks := rm.pubs[p.id]
	delete(rm.pubs, p.id)
	rm.mu.Unlock()

	if len(deadTracks) > 0 {
		subs := rm.others(p.id)
		for _, sub := range subs {
			sub.sendersMu.Lock()
			for trackID := range deadTracks {
				k := senderKey(p.id, trackID)
				if snd, ok := sub.senders[k]; ok {
					_ = sub.pc.RemoveTrack(snd)
					delete(sub.senders, k)
				}
				delete(sub.localVideo, k)
				delete(sub.localAudio, k)
			}
			sub.sendersMu.Unlock()
			requestNegotiation(sub)
		}
	}

	rm.broadcastExcept(p.id, sfuMessage{
		Type: "peer-left",
		From: p.id, // this matches the stream id you used as pubID
	})

	// Cleanup happens after readPump returns
	rm.delPeer(p.id)
	close(p.send)
	_ = p.conn.Close()
	_ = p.pc.Close()
	log.Printf("[SFU] peer %s left room %s", p.id, p.room)
}

/* --------------------------- Pion event handlers --------------------------- */

func wirePeerEvents(p *sfuPeer, rm *sfuRoom) {
	// Server → client trickle ICE
	p.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		reply := sfuMessage{Type: "candidate", Candidate: ptr(c.ToJSON())}
		sendJSON(p, reply)
	})

	p.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[SFU] ICE %s peer=%s", state.String(), p.id)
		if state == webrtc.ICEConnectionStateFailed {
			go sfuRestartICE(p)
		}
	})

	p.pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) {
		if s == webrtc.ICEGatheringStateComplete {
			sendJSON(p, sfuMessage{Type: "candidate", Candidate: nil}) // end-of-candidates
		}
	})

	// Publisher track arrived → create per-subscriber local tracks and renegotiate them
	p.pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		pubID := p.id
		trackID := remote.ID()
		kind := remote.Kind()
		log.Printf("[SFU] publish %s %s by %s", kind.String(), trackID, pubID)

		// Register publisher track in room state
		rm.mu.Lock()
		if _, ok := rm.pubs[pubID]; !ok {
			rm.pubs[pubID] = make(map[string]*pubTrack)
		}
		rm.pubs[pubID][trackID] = &pubTrack{
			remote:  remote,
			kind:    kind,
			pubID:   pubID,
			trackID: trackID,
			pubPC:   p.pc,
		}
		rm.mu.Unlock()

		mime := remote.Codec().MimeType
		log.Printf("[SFU] incoming %s codec: %s", kind.String(), mime)
		if mime != webrtc.MimeTypeH264 && kind != webrtc.RTPCodecTypeAudio {
			log.Printf("[CV] unsupported input codec %s (expecting H264). No processed video will be produced.", mime)
			return
		}

		// For each other peer in the room, create an outbound track + sender (NORMAL FAN-OUT)
		others := rm.others(p.id)
		for _, sub := range others {
			codec := remote.Codec().RTPCodecCapability

			if kind == webrtc.RTPCodecTypeVideo || kind == webrtc.RTPCodecTypeAudio {
				out, err := webrtc.NewTrackLocalStaticRTP(codec, trackID, pubID)
				if err != nil {
					continue
				}
				sender, err := sub.pc.AddTrack(out)
				if err != nil {
					continue
				}

				key := senderKey(pubID, trackID)
				sub.sendersMu.Lock()
				sub.senders[key] = sender
				if kind == webrtc.RTPCodecTypeVideo {
					sub.localVideo[key] = out
				} else {
					sub.localAudio[key] = out
				}
				sub.sendersMu.Unlock()

				go relayRTCPToPublisher(sender, remote, p.pc)
				requestNegotiation(sub)
			}
		}

		// ---------- spin up CV pipeline (metadata only; NO processed track) ----------
		if kind == webrtc.RTPCodecTypeVideo {
			key := senderKey(pubID, trackID)
			inPort := 8000 + rand.Intn(1000) // RTP IN for decoder

			cfg := cvpipe.Config{
				Key:       key,
				CodecCap:  remote.Codec().RTPCodecCapability,
				W:         1280,
				H:         720,
				FPS:       30,
				InRTPPort: inPort,
				InPT:      uint8(remote.Codec().PayloadType),

				// Optional: you can keep OutRTPPort/encoder for diagnostics; safe to remove if unused.
				// OutRTPPort: 7000 + rand.Intn(1000),
				H264Bitrate: "2500k",

				PubID:   pubID,
				TrackID: trackID,
			}

			log.Printf("[CV] start cfg pub=%s track=%s inPT=%d inPort=%d size=%dx%d fps=%d",
				pubID, trackID, cfg.InPT, cfg.InRTPPort, cfg.W, cfg.H, cfg.FPS)

			pl, err := cvpipe.StartH264(context.Background(), cfg)
			if err != nil {
				log.Printf("[CV] start failed for %s: %v", key, err)
			} else {
				// Nudge keyframes just to get the decoder rolling quickly
				ssrc := remote.SSRC()
				go burstKeyframes(p.pc, uint32(ssrc), 3, 200*time.Millisecond)

				p.procMu.Lock()
				p.procPipes[key] = pl
				p.procMu.Unlock()

				// Forward only metadata (face boxes) to clients
				go forwardBoxesToRoom(rm, p.id, pl.Boxes, pubID, trackID)
			}
		}

		gateKey := senderKey(pubID, trackID)
		trackSSRC := remote.SSRC()

		p.keyframeMu.Lock()
		if p.keyframeGate == nil {
			p.keyframeGate = make(map[string]*keyGate)
		}
		p.keyframeGate[gateKey] = &keyGate{
			waiting: true,
			lastPLI: time.Now().Add(-time.Second),
		}
		p.keyframeMu.Unlock()

		// --------- single-reader fan-out + CV tee (no processed AddTrack) ----------
		go func() {
			firstWrite := true
			waitLogLast := time.Now().Add(-3 * time.Second)

			for {
				pkt, _, err := remote.ReadRTP()
				if err != nil {
					break
				}

				subs := rm.others(p.id)

				// ----- RAW AUDIO FAN-OUT -----
				if kind == webrtc.RTPCodecTypeAudio {
					for _, sub := range subs {
						k := senderKey(pubID, trackID)
						if out := sub.localAudio[k]; out != nil {
							_ = out.WriteRTP(pkt)
						}
					}
					continue
				}

				// ----- RAW VIDEO FAN-OUT -----
				for _, sub := range subs {
					k := senderKey(pubID, trackID)
					if out := sub.localVideo[k]; out != nil {
						_ = out.WriteRTP(pkt)
					}
				}

				// ----- CV DECODER TEE (metadata only) -----
				p.procMu.Lock()
				pl := p.procPipes[senderKey(pubID, trackID)]
				p.procMu.Unlock()
				if pl == nil || pl.InRTPConn == nil {
					// decoder not ready yet; skip feeding it, but keep raw fan-out above
					continue
				}

				// KEYFRAME GATE (only for feeding the decoder)
				p.keyframeMu.Lock()
				g := p.keyframeGate[gateKey]
				p.keyframeMu.Unlock()
				if g == nil {
					p.keyframeMu.Lock()
					g = &keyGate{waiting: true, lastPLI: time.Now().Add(-time.Second)}
					p.keyframeGate[gateKey] = g
					p.keyframeMu.Unlock()
				}

				if g.waiting {
					if time.Since(waitLogLast) > 2*time.Second {
						log.Printf("[CV] waiting for first keyframe (pub=%s track=%s)", pubID, trackID)
						waitLogLast = time.Now()
					}
					if time.Since(g.lastPLI) > 300*time.Millisecond {
						_ = requestKeyframePLI(p.pc, uint32(trackSSRC))
						p.keyframeMu.Lock()
						g.lastPLI = time.Now()
						p.keyframeMu.Unlock()
					}
					if !isH264KeyframeRTP(pkt.Payload) {
						continue
					}
					p.keyframeMu.Lock()
					g.waiting = false
					p.keyframeMu.Unlock()
					log.Printf("[CV] keyframe detected; starting decode (pub=%s track=%s)", pubID, trackID)
				}

				// Feed decoder
				b, err := pkt.Marshal()
				if err != nil {
					log.Printf("[CV] marshal RTP failed: %v", err)
					continue
				}
				if _, err := pl.InRTPConn.Write(b); err != nil {
					log.Printf("[CV] write to decoder udp failed: %v", err)
					continue
				}
				if firstWrite {
					log.Printf("[CV] first RTP → decoder delivered (pub=%s track=%s)", pubID, trackID)
					firstWrite = false
				}
			}

			// ---------- publisher track cleanup ----------
			rm.mu.Lock()
			if tracks, ok := rm.pubs[pubID]; ok {
				delete(tracks, trackID)
				if len(tracks) == 0 {
					delete(rm.pubs, pubID)
				}
			}
			rm.mu.Unlock()

			// Remove RAW fan-out tracks from subscribers
			subs := rm.others(p.id)
			for _, sub := range subs {
				k := senderKey(pubID, trackID)
				sub.sendersMu.Lock()
				if snd, ok := sub.senders[k]; ok {
					_ = sub.pc.RemoveTrack(snd)
					delete(sub.senders, k)
				}
				if kind == webrtc.RTPCodecTypeVideo {
					delete(sub.localVideo, k)
				} else {
					delete(sub.localAudio, k)
				}
				sub.sendersMu.Unlock()
				requestNegotiation(sub)
			}

			// Tear down CV pipeline (video only) — NO processed-track cleanup
			if kind == webrtc.RTPCodecTypeVideo {
				key := senderKey(pubID, trackID)
				p.procMu.Lock()
				pl := p.procPipes[key]
				delete(p.procPipes, key)
				p.procMu.Unlock()
				if pl != nil {
					pl.Stop()
				}
			}

			// Remove the keyframe gate for this track
			p.keyframeMu.Lock()
			delete(p.keyframeGate, gateKey)
			p.keyframeMu.Unlock()
		}()

	})

	p.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			_ = p.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			_ = p.conn.Close()
		}
	})

}

/* --------------------------- Negotiation machinery -------------------------- */

func requestNegotiation(p *sfuPeer) {
	select {
	case p.negCh <- struct{}{}: // signal
	default: // coalesce
	}
}

func negotiatorWorker(p *sfuPeer) {
	const debounce = 25 * time.Millisecond

	waitStable := func() bool {
		for {
			if p.pc.SignalingState() == webrtc.SignalingStateStable {
				return true
			}
			select {
			case <-p.closed:
				return false
			case <-time.After(15 * time.Millisecond):
			}
		}
	}

	for {
		select {
		case <-p.closed:
			return
		case <-p.negCh:
		}

		deadline := time.NewTimer(debounce)
	coalesce:
		for {
			select {
			case <-p.closed:
				deadline.Stop()
				return
			case <-p.negCh:
			case <-deadline.C:
				break coalesce
			}
		}

		if !waitStable() {
			return
		}

		p.makingOffer.Store(true) // <<< mark glare risk window
		offer, err := p.pc.CreateOffer(nil)
		if err != nil {
			p.makingOffer.Store(false)
			continue
		}
		if p.pc.SignalingState() != webrtc.SignalingStateStable {
			p.makingOffer.Store(false)
			continue
		}
		if err := p.pc.SetLocalDescription(offer); err != nil {
			p.makingOffer.Store(false)
			continue
		}
		p.makingOffer.Store(false)

		if ld := p.pc.LocalDescription(); ld != nil {
			sendJSON(p, sfuMessage{Type: "offer", Offer: ld})
		}
	}
}

/* --------------------------- WS read/write pumps --------------------------- */

func writePumpSFU(p *sfuPeer) {
	defer func() {
		close(p.closed)
		_ = p.conn.Close()
	}()

	for msg := range p.send {
		if err := p.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("[SFU] write error: %v", err)
			return
		}
	}
}

func readPumpSFU(p *sfuPeer, rm *sfuRoom) {
	const maxCandQueue = 4096

	defer func() {
		// On reader exit, let SfuWebsocketHandler handle final cleanup
	}()

	for {
		_, raw, err := p.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg sfuMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[SFU] bad JSON: %v", err)
			continue
		}

		switch msg.Type {
		case "offer":
			offer := *msg.Offer

			// Offer glare handling:
			// If we’re making an offer or not stable, and we are IMPOLITE, ignore.
			offerCollision := p.makingOffer.Load() || p.pc.SignalingState() != webrtc.SignalingStateStable
			if offerCollision && !p.polite {
				// Impolite peer ignores the incoming offer
				log.Printf("[SFU] glare: ignoring remote offer while have-local-offer (impolite)")
				break
			}

			// If polite and there is a collision, roll back first.
			if offerCollision {
				_ = p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback})
			}

			if err := p.pc.SetRemoteDescription(offer); err != nil {
				log.Printf("[SFU] SetRemoteDescription(offer) err: %v", err)
				break
			}

			p.candMu.Lock()
			p.remoteSet = true
			for _, c := range p.candQueue {
				_ = p.pc.AddICECandidate(c)
			}
			p.candQueue = nil
			p.candMu.Unlock()

			answer, err := p.pc.CreateAnswer(nil)
			if err != nil {
				log.Printf("[SFU] CreateAnswer err: %v", err)
				break
			}
			if err := p.pc.SetLocalDescription(answer); err != nil {
				log.Printf("[SFU] SetLocalDescription(answer) err: %v", err)
				break
			}
			sendJSON(p, sfuMessage{Type: "answer", Answer: p.pc.LocalDescription()})
		case "answer":
			// Server had sent an offer → client answers
			ans := *msg.Answer
			if err := p.pc.SetRemoteDescription(ans); err != nil {
				log.Printf("[SFU] SetRemoteDescription(answer) err: %v", err)
				continue
			}
			p.candMu.Lock()
			p.remoteSet = true
			for _, c := range p.candQueue {
				_ = p.pc.AddICECandidate(c)
			}
			p.candQueue = nil
			p.candMu.Unlock()

		case "candidate":
			if msg.Candidate == nil {
				_ = p.pc.AddICECandidate(webrtc.ICECandidateInit{})
				continue
			}
			ice := *msg.Candidate
			p.candMu.Lock()
			if !p.remoteSet || p.pc.RemoteDescription() == nil {
				if len(p.candQueue) < maxCandQueue {
					p.candQueue = append(p.candQueue, ice)
				}
				p.candMu.Unlock()
				continue
			}
			p.candMu.Unlock()
			if err := p.pc.AddICECandidate(ice); err != nil {
				log.Printf("[SFU] AddICECandidate err: %v", err)
			}

		case "leave":
			return
		}
	}
}

/* ---------------------------- ICE restart (server) ---------------------------- */

func sfuRestartICE(p *sfuPeer) {
	p.restartMu.Lock()
	if p.iceRestartIn {
		p.restartMu.Unlock()
		return
	}
	p.iceRestartIn = true
	p.restartMu.Unlock()

	defer func() {
		p.restartMu.Lock()
		p.iceRestartIn = false
		p.restartMu.Unlock()
	}()

	if p.pc.SignalingState() != webrtc.SignalingStateStable {
		return
	}
	offer, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return
	}

	if err := p.pc.SetLocalDescription(offer); err != nil {
		return
	}
	sendJSON(p, sfuMessage{Type: "offer", Offer: p.pc.LocalDescription()})
}

/* ----------------------------- Utilities & RTCP ----------------------------- */

func senderKey(pubID, trackID string) string {
	return pubID + "|" + trackID
}

func sendJSON(p *sfuPeer, v interface{}) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case p.send <- raw:
	case <-p.closed:
	default:
		log.Printf("[SFU] send queue overflow for %s; dropping", p.id)
	}
}

func ptr[T any](v T) *T { return &v }

// Relay PLIs from a subscriber's RTPSender back to the publisher PC.
func relayRTCPToPublisher(subSender *webrtc.RTPSender, pubTrack *webrtc.TrackRemote, pubPC *webrtc.PeerConnection) {
	if pubPC == nil || pubTrack == nil {
		return
	}
	for {
		pkts, n, err := subSender.ReadRTCP()
		if err != nil {
			return
		}
		_ = n
		for _, pkt := range pkts {
			switch p := pkt.(type) {
			case *rtcp.PictureLossIndication:
				p.MediaSSRC = uint32(pubTrack.SSRC())
				_ = pubPC.WriteRTCP([]rtcp.Packet{p})
			case *rtcp.FullIntraRequest:
				p.MediaSSRC = uint32(pubTrack.SSRC())
				_ = pubPC.WriteRTCP([]rtcp.Packet{p})
			}
		}
	}
}

/* --------------------------------- Helpers --------------------------------- */

func randomSFUID() string {
	return fmt.Sprintf("sfu-%d", rand.Intn(100000))
}

func (r *sfuRoom) broadcastExcept(senderID string, msg interface{}) {
	r.mu.Lock()
	subs := make([]*sfuPeer, 0, len(r.peers))
	for id, p := range r.peers {
		if id != senderID {
			subs = append(subs, p)
		}
	}
	r.mu.Unlock()
	for _, sub := range subs {
		sendJSON(sub, msg)
	}
}

// --- Keyframe helpers ---
func requestKeyframe(pc *webrtc.PeerConnection, ssrc uint32, seq uint8) {
	_ = pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: ssrc},
		&rtcp.FullIntraRequest{FIR: []rtcp.FIREntry{{SSRC: ssrc, SequenceNumber: seq}}},
	})
}

func burstKeyframes(pc *webrtc.PeerConnection, ssrc uint32, count int, spacing time.Duration) {
	for i := 0; i < count; i++ {
		requestKeyframe(pc, ssrc, uint8(i+1))
		time.Sleep(spacing)
	}
}
func isH264KeyframeRTP(payload []byte) bool {
	if len(payload) < 1 {
		return false
	}
	nal := payload[0] & 0x1F
	switch nal {
	case 5: // IDR
		return true
	case 24: // STAP-A
		// scan aggregated NALs
		i := 1
		for i+2 <= len(payload) {
			if i+2 > len(payload) {
				break
			}
			size := int(binary.BigEndian.Uint16(payload[i : i+2]))
			i += 2
			if i+size > len(payload) {
				break
			}
			if size > 0 {
				t := payload[i] & 0x1F
				if t == 5 {
					return true
				}
			}
			i += size
		}
		return false
	case 28: // FU-A
		if len(payload) < 2 {
			return false
		}
		fuHeader := payload[1]
		start := (fuHeader & 0x80) != 0
		orig := fuHeader & 0x1F
		return start && orig == 5
	default:
		return false
	}
}

func requestKeyframePLI(pc *webrtc.PeerConnection, ssrc uint32) error {
	return pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: ssrc},
	})
}

func forwardBoxesToRoom(rm *sfuRoom, pubPeerID string, ch <-chan cvpipe.BoxesEvent, pubID, trackID string) {
	for ev := range ch {
		// shape a message your frontend expects
		msg := map[string]any{
			"type":    "cv/boxes",
			"from":    pubID,
			"trackId": trackID,
			"w":       ev.W,
			"h":       ev.H,
			"ts":      ev.TsUnixMs,
			"boxes":   ev.Boxes,
		}

		// Broadcast to others in room
		others := rm.others(pubPeerID)
		for _, sub := range others {
			sendJSON(sub, msg)
		}
	}
}

// add this to your SFU package

func (rm *sfuRoom) attachRawFanoutToSubscriber(sub *sfuPeer) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	for pubID, tracks := range rm.pubs {
		// Don't mirror their own tracks back
		if pubID == sub.id {
			continue
		}
		for trackID, pt := range tracks {
			codec := pt.remote.Codec().RTPCodecCapability
			key := senderKey(pubID, trackID)

			// Skip if already attached (rejoin / renegotiate safety)
			sub.sendersMu.Lock()
			_, already := sub.senders[key]
			sub.sendersMu.Unlock()
			if already {
				continue
			}

			out, err := webrtc.NewTrackLocalStaticRTP(codec, trackID, pubID)
			if err != nil {
				continue
			}
			snd, err := sub.pc.AddTrack(out)
			if err != nil {
				continue
			}

			// book-keeping
			sub.sendersMu.Lock()
			sub.senders[key] = snd
			if pt.kind == webrtc.RTPCodecTypeVideo {
				sub.localVideo[key] = out
			} else {
				sub.localAudio[key] = out
			}
			sub.sendersMu.Unlock()

			// make sure RTCP from sub reaches the publisher
			go relayRTCPToPublisher(snd, pt.remote, pt.pubPC)

			// trigger negotiation for the new m-section
			requestNegotiation(sub)
		}
	}
}
