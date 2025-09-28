package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cvproc "github.com/n0remac/robot-webrtc/cvpipe"
	wsock "github.com/n0remac/robot-webrtc/websocket"
	"github.com/pion/interceptor"
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

	procMu     sync.Mutex
	procUDP    map[string]*net.UDPConn                // key pubID|trackID → UDP socket to pipeline input
	procTracks map[string]*webrtc.TrackLocalStaticRTP // server-published processed track
	procPipes  map[string]*cvproc.Pipeline
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
	// Robust: register all browser-common codecs (dynamic PTs negotiated by SDP)
	if err := m.RegisterDefaultCodecs(); err != nil {
		panic(err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		panic(err)
	}
	return webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithInterceptorRegistry(ir),
	)
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
		id:         id,
		room:       room,
		conn:       conn,
		send:       make(chan []byte, 256), // bounded like your hub
		pc:         pc,
		senders:    make(map[string]*webrtc.RTPSender),
		localVideo: make(map[string]*webrtc.TrackLocalStaticRTP),
		localAudio: make(map[string]*webrtc.TrackLocalStaticRTP),
		negCh:      make(chan struct{}, 1),
		closed:     make(chan struct{}),
		procUDP:    make(map[string]*net.UDPConn),
		procPipes:  make(map[string]*cvproc.Pipeline),
		procTracks: make(map[string]*webrtc.TrackLocalStaticRTP),
	}

	p.negOnce.Do(func() { go negotiatorWorker(p) })

	rm := sfu.getRoom(room)
	rm.addPeer(p)

	// If there are existing publishers in the room, attach their tracks to this new peer
	attachExistingPublishersTo(p, rm)

	// Wire Pion events
	wirePeerEvents(p, rm)

	// Start writer (single goroutine) before we may send anything
	go writePumpSFU(p)

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

		// For each other peer in the room, create an outbound track + sender (normal fan-out)
		others := rm.others(p.id)
		for _, sub := range others {
			codec := remote.Codec().RTPCodecCapability
			out, err := webrtc.NewTrackLocalStaticRTP(codec, trackID, pubID)
			if err != nil {
				log.Printf("[SFU] create local track failed: %v", err)
				continue
			}
			sender, err := sub.pc.AddTrack(out)
			if err != nil {
				log.Printf("[SFU] AddTrack to %s failed: %v", sub.id, err)
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

			// Optional: relay RTCP PLIs/FIRs back to publisher
			go relayRTCPToPublisher(sender, remote, p.pc)

			requestNegotiation(sub) // coalesced
		}

		// ---------- spin up a CV pipeline and a server-published processed track ----------
		var (
			procKey = senderKey(pubID, trackID) // use same key for proc maps
		)
		if kind == webrtc.RTPCodecTypeVideo {
			key := senderKey(pubID, trackID)
			// Allocate a local UDP port for encoder RTP output
			outPort := 7000 + rand.Intn(1000)

			cfg := cvproc.Config{
				Key: key,
				W:   1280, H: 720, FPS: 30,
				CodecPT:     uint8(remote.Codec().PayloadType),
				OutPT:       uint8(remote.Codec().PayloadType),
				OutTrackID:  trackID + "-proc",
				OutStreamID: "server-proc",
				OutRTPPort:  outPort,
				H264Bitrate: "2500k",
			}

			// Start pipeline
			pl, err := cvproc.StartH264(context.Background(), remote, sfu.api, cfg)
			if err != nil {
				log.Printf("[CV] start failed for %s: %v", key, err)
			} else {
				// Attach processed track to all *other* peers
				others := rm.others(p.id)
				for _, sub := range others {
					snd, err := sub.pc.AddTrack(pl.TrackOut)
					if err != nil {
						log.Printf("[CV] add proc track to %s failed: %v", sub.id, err)
						continue
					}
					k2 := senderKey("server-proc", cfg.OutTrackID)
					sub.sendersMu.Lock()
					sub.senders[k2] = snd
					sub.localVideo[k2] = pl.TrackOut
					sub.sendersMu.Unlock()
					requestNegotiation(sub)
				}

				p.procMu.Lock()
				p.procPipes[key] = pl
				p.procTracks[key] = pl.TrackOut
				p.procMu.Unlock()
			}
		}
		// ---------- end NEW ----------

		// Forward RTP from publisher to all subscribers' local tracks (+ mirror to CV input)
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, err := remote.Read(buf)
				if err != nil {
					break
				}
				// 1) Normal SFU fan-out
				subs := rm.others(p.id)
				for _, sub := range subs {
					var out *webrtc.TrackLocalStaticRTP
					k := senderKey(pubID, trackID)
					if kind == webrtc.RTPCodecTypeVideo {
						out = sub.localVideo[k]
					} else {
						out = sub.localAudio[k]
					}
					if out != nil {
						_, _ = out.Write(buf[:n])
					}
				}

				// 2) NEW: mirror raw publisher RTP to CV pipeline input
				if kind == webrtc.RTPCodecTypeVideo {
					p.procMu.Lock()
					udp := p.procUDP[procKey]
					p.procMu.Unlock()
					if udp != nil {
						_, _ = udp.Write(buf[:n])
					}
				}
			}

			// Cleanup publisher registration
			rm.mu.Lock()
			if tracks, ok := rm.pubs[pubID]; ok {
				delete(tracks, trackID)
				if len(tracks) == 0 {
					delete(rm.pubs, pubID)
				}
			}
			rm.mu.Unlock()

			// Remove normal fan-out tracks from subscribers
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

			// tear down CV pipeline and remove processed track
			if kind == webrtc.RTPCodecTypeVideo {
				key := senderKey(pubID, trackID)

				p.procMu.Lock()
				pl := p.procPipes[key]
				delete(p.procPipes, key)
				delete(p.procTracks, key)
				p.procMu.Unlock()

				if pl != nil {
					pl.Stop()
				}

				subs := rm.others(p.id)
				for _, sub := range subs {
					k2 := senderKey("server-proc", trackID+"-proc")
					sub.sendersMu.Lock()
					if snd, ok := sub.senders[k2]; ok {
						_ = sub.pc.RemoveTrack(snd)
						delete(sub.senders, k2)
					}
					delete(sub.localVideo, k2)
					sub.sendersMu.Unlock()
					requestNegotiation(sub)
				}
			}

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
	// Small debounce so multiple AddTrack/AddTransceiver events coalesce into one offer
	const debounce = 25 * time.Millisecond

	waitStable := func() bool {
		// Spin until stable or closed
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
		// Block until someone requests negotiation or we are closed
		select {
		case <-p.closed:
			return
		case <-p.negCh:
		}

		// Debounce/coalesce any immediate follow-up signals
		deadline := time.NewTimer(debounce)
	coalesce:
		for {
			select {
			case <-p.closed:
				deadline.Stop()
				return
			case <-p.negCh:
				// keep coalescing
			case <-deadline.C:
				break coalesce
			}
		}

		// 1) Be stable before creating an offer (avoid self-glare)
		if !waitStable() {
			return
		}

		// 2) Create the offer
		offer, err := p.pc.CreateOffer(nil)
		if err != nil {
			// Try again on the next negotiation request
			continue
		}

		// 3) If we became unstable during offer creation, drop this one
		if p.pc.SignalingState() != webrtc.SignalingStateStable {
			continue
		}

		// 4) Try to set local description; if this races with an incoming offer,
		//    SetLocalDescription can fail — simply retry on the next signal.
		if err := p.pc.SetLocalDescription(offer); err != nil {
			continue
		}

		// 5) Send the exact SDP we set (ensures ICE ufrag/pwd, DTLS fp present)
		if ld := p.pc.LocalDescription(); ld != nil {
			sendJSON(p, sfuMessage{
				Type:  "offer",
				Offer: ld,
			})
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
			// Client is offering → server answers
			offer := *msg.Offer
			// If not stable, roll back before new offer (glare-safe)
			if p.pc.SignalingState() != webrtc.SignalingStateStable {
				_ = p.pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback})
			}
			if err := p.pc.SetRemoteDescription(offer); err != nil {
				log.Printf("[SFU] SetRemoteDescription(offer) err: %v", err)
				continue
			}
			p.candMu.Lock()
			p.remoteSet = true
			// Flush queued ICE candidates
			for _, c := range p.candQueue {
				_ = p.pc.AddICECandidate(c)
			}
			p.candQueue = nil
			p.candMu.Unlock()

			answer, err := p.pc.CreateAnswer(nil)
			if err != nil {
				log.Printf("[SFU] CreateAnswer err: %v", err)
				continue
			}
			if err := p.pc.SetLocalDescription(answer); err != nil {
				log.Printf("[SFU] SetLocalDescription(answer) err: %v", err)
				continue
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

func attachExistingPublishersTo(sub *sfuPeer, rm *sfuRoom) {
	rm.mu.Lock()
	// copy pointers you need while holding lock
	pubs := make([]*pubTrack, 0, 8)
	for _, tracks := range rm.pubs {
		for _, pt := range tracks {
			pubs = append(pubs, pt)
		}
	}
	rm.mu.Unlock()

	for _, pt := range pubs {
		// Don't attach a user's own published tracks back to themselves
		if pt.pubID == sub.id {
			continue
		}

		codec := pt.remote.Codec().RTPCodecCapability
		out, err := webrtc.NewTrackLocalStaticRTP(codec, pt.trackID, pt.pubID)
		if err != nil {
			continue
		}

		sender, err := sub.pc.AddTrack(out)
		if err != nil {
			continue
		}

		key := senderKey(pt.pubID, pt.trackID)
		sub.sendersMu.Lock()
		sub.senders[key] = sender
		if pt.kind == webrtc.RTPCodecTypeVideo {
			sub.localVideo[key] = out
		} else {
			sub.localAudio[key] = out
		}
		sub.sendersMu.Unlock()

		// optional: PLI/FIR relay
		go relayRTCPToPublisher(sender, pt.remote, pt.pubPC)
	}

	// Ask subscriber to renegotiate once (coalesced)
	requestNegotiation(sub)
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
