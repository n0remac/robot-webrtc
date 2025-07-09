package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type sfuRoom struct {
	mu      sync.Mutex
	clients map[string]*sfuClient
}

type sfuClient struct {
	id               string
	conn             *websocket.Conn
	pc               *webrtc.PeerConnection
	inTracks         map[string]*webrtc.TrackRemote
	localTracks      map[string]*webrtc.TrackLocalStaticRTP
	candQueue        []webrtc.ICECandidateInit
	restartMu        sync.Mutex
	hasFailedICE     bool
	needsNegotiation bool
	send             chan interface{}
}

// Single room for now, could make map[string]*sfuRoom for multi-room
var sfuRoomHub = &sfuRoom{clients: make(map[string]*sfuClient)}

var sfuIceServers = []webrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
	// TODO: add dynamic TURN config if needed
}

// ---- Helper: create compatible MediaEngine (browser codecs) ----
func newSFUAPI() *webrtc.API {
	m := webrtc.MediaEngine{}
	// Explicitly support H264 and Opus for browser compatibility
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    "video/H264",
			ClockRate:   90000,
			SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f",
		},
		PayloadType: 109,
	}, webrtc.RTPCodecTypeVideo)
	m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio)
	return webrtc.NewAPI(webrtc.WithMediaEngine(&m))
}

// ---- Handler ----
func SfuWebsocketHandler(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "WebSocket upgrade failed", http.StatusBadRequest)
		return
	}
	defer ws.Close()

	api := newSFUAPI()
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: sfuIceServers})
	if err != nil {
		log.Println("SFU: PC create error:", err)
		return
	}
	defer pc.Close()

	clientID := r.URL.Query().Get("id")
	if clientID == "" {
		clientID = randomSFUID()
	}

	myClient := &sfuClient{
		id:          clientID,
		conn:        ws,
		pc:          pc,
		inTracks:    make(map[string]*webrtc.TrackRemote),
		localTracks: make(map[string]*webrtc.TrackLocalStaticRTP),
		candQueue:   []webrtc.ICECandidateInit{},
		send:        make(chan interface{}, 32),
	}

	go func() {
		for msg := range myClient.send {
			if err := myClient.conn.WriteJSON(msg); err != nil {
				log.Printf("[SFU] WriteJSON error for %s: %v", myClient.id, err)
				return // On error, exit goroutine (you may want to handle reconnection)
			}
		}
	}()

	pc.OnSignalingStateChange(func(state webrtc.SignalingState) {
		go triggerNegotiationIfStable(myClient)
	})

	// Register ICE event handlers for robustness
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[SFU] ICE state for %s: %s", myClient.id, state.String())
		if state == webrtc.ICEConnectionStateFailed && !myClient.hasFailedICE {
			go sfuRestartICE(myClient)
		}
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candidate := map[string]interface{}{
			"type":      "candidate",
			"candidate": c.ToJSON(),
		}
		myClient.send <- candidate
	})

	// --- Add this client to room ---
	sfuRoomHub.mu.Lock()
	sfuRoomHub.clients[myClient.id] = myClient
	others := make([]*sfuClient, 0, len(sfuRoomHub.clients)-1)
	for _, c := range sfuRoomHub.clients {
		if c.id != myClient.id {
			others = append(others, c)
		}
	}
	sfuRoomHub.mu.Unlock()

	log.Printf("[SFU] Client %s connected. Total now: %d", myClient.id, len(sfuRoomHub.clients))

	// --- Add existing tracks to new client ---
	for _, other := range others {
		for _, t := range other.inTracks {
			addSFUTrackToPeer(other, myClient, t)
		}
	}

	// --- Relay incoming tracks to all other clients ---
	pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("[SFU] Client %s published new %s track %s", myClient.id, remote.Kind().String(), remote.ID())
		myClient.inTracks[remote.ID()] = remote

		// Fan out: add this new track to all other clients
		sfuRoomHub.mu.Lock()
		for _, other := range sfuRoomHub.clients {
			if other.id == myClient.id {
				continue
			}
			addSFUTrackToPeer(myClient, other, remote)
			go triggerNegotiationIfStable(other)
		}
		sfuRoomHub.mu.Unlock()

		// Pull RTP packets and write to all associated local tracks with retries
		go func() {
			buf := make([]byte, 1500)
			for {
				n, _, readErr := remote.Read(buf)
				if readErr != nil {
					log.Printf("[SFU] Track %s read error: %v", remote.ID(), readErr)
					break
				}
				sfuRoomHub.mu.Lock()
				for _, other := range sfuRoomHub.clients {
					if other.id == myClient.id {
						continue
					}
					lt := other.getLocalTrackFor(remote)
					if lt != nil {
						// Retry until SRTP context is ready, up to timeout
						for tries := 0; tries < 10; tries++ {
							if _, err := lt.Write(buf[:n]); err != nil {
								log.Printf("[SFU] RTP write err for %s: %v (try %d)", remote.ID(), err, tries)
								time.Sleep(50 * time.Millisecond)
								continue
							}
							break
						}
					}
				}
				sfuRoomHub.mu.Unlock()
			}
		}()
	})

	// --- ICE candidate buffering, SDP negotiation ---
	var remoteDescSet bool
	var candQueueMu sync.Mutex

	// --- Handle signaling messages from client ---
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break // client gone
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		typ, _ := msg["type"].(string)
		switch typ {
		case "offer":
			offer := decodeSDP(msg["offer"])
			state := pc.SignalingState()
			if state != webrtc.SignalingStateStable {
				// Rollback local description so we can accept the new offer
				log.Printf("[SFU] In %s, rolling back before accepting new offer from %s", state, myClient.id)
				if err := pc.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
					log.Printf("SFU: Rollback failed: %v", err)
					// If rollback fails, best effort: continue and try to set remote desc anyway
				}
			}

			if err := pc.SetRemoteDescription(offer); err != nil {
				log.Println("SFU: SetRemoteDescription error:", err)
				continue
			}
			remoteDescSet = true
			// Flush any queued candidates now that remote desc is set
			candQueueMu.Lock()
			for _, cand := range myClient.candQueue {
				if err := pc.AddICECandidate(cand); err != nil {
					log.Printf("SFU: Queued AddICECandidate error: %v", err)
				}
			}
			myClient.candQueue = nil
			candQueueMu.Unlock()

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				log.Println("SFU: CreateAnswer error:", err)
				continue
			}
			if err := pc.SetLocalDescription(answer); err != nil {
				log.Println("SFU: SetLocalDescription error:", err)
				continue
			}
			resp := map[string]interface{}{
				"type":   "answer",
				"answer": pc.LocalDescription(),
			}
			myClient.send <- resp
			log.Printf("[SFU] Offer accepted and answered for %s", myClient.id)

			// After sending answer, attempt negotiation if anything is queued
			go triggerNegotiationIfStable(myClient)
		case "answer":
			log.Printf("[SFU] Got answer from %s, state before: %s", myClient.id, pc.SignalingState())
			ans := decodeSDP(msg["answer"])
			if err := pc.SetRemoteDescription(ans); err != nil {
				log.Println("SFU: SetRemoteDescription error (answer):", err)
				continue
			}
			log.Printf("[SFU] Applied answer for %s, state now: %s", myClient.id, pc.SignalingState())

			// Optionally flush candidates here, if you have any buffered (unlikely, but safe):
			candQueueMu.Lock()
			for _, cand := range myClient.candQueue {
				if err := pc.AddICECandidate(cand); err != nil {
					log.Printf("SFU: Queued AddICECandidate error: %v", err)
				}
			}
			myClient.candQueue = nil
			candQueueMu.Unlock()

			// After answer, check for queued negotiation
			go triggerNegotiationIfStable(myClient)

		case "candidate":
			cand := decodeICE(msg["candidate"])
			if !remoteDescSet || pc.RemoteDescription() == nil {
				// Buffer until remote desc is set
				candQueueMu.Lock()
				myClient.candQueue = append(myClient.candQueue, cand)
				candQueueMu.Unlock()
				log.Printf("[SFU] Buffered ICE candidate from %s (remote desc not set)", myClient.id)
			} else {
				if err := pc.AddICECandidate(cand); err != nil {
					log.Printf("SFU: AddICECandidate error: %v", err)
				}
			}
		case "leave":
			log.Printf("[SFU] Client %s left", myClient.id)
			break
		}
	}

	// --- Cleanup on disconnect ---
	sfuRoomHub.mu.Lock()
	delete(sfuRoomHub.clients, myClient.id)
	sfuRoomHub.mu.Unlock()
	log.Printf("[SFU] Client %s cleaned up.", myClient.id)
	close(myClient.send)
}

func addSFUTrackToPeer(from *sfuClient, to *sfuClient, remote *webrtc.TrackRemote) {
	to.restartMu.Lock()
	defer to.restartMu.Unlock()

	lt, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability, remote.ID(), remote.StreamID())
	if err != nil {
		log.Printf("[SFU] Failed to create local track: %v", err)
		return
	}
	_, err = to.pc.AddTrack(lt)
	if err != nil {
		log.Printf("[SFU] AddTrack failed: %v", err)
		return
	}
	to.localTracks[remote.ID()] = lt
	to.needsNegotiation = true
}

func triggerNegotiationIfStable(c *sfuClient) {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()
	if c.needsNegotiation && c.pc.SignalingState() == webrtc.SignalingStateStable {
		c.needsNegotiation = false
		offer, err := c.pc.CreateOffer(nil)
		if err != nil {
			log.Printf("[SFU] CreateOffer failed: %v", err)
			return
		}
		if err := c.pc.SetLocalDescription(offer); err != nil {
			log.Printf("[SFU] SetLocalDescription failed: %v", err)
			return
		}
		c.send <- map[string]interface{}{"type": "offer", "offer": c.pc.LocalDescription()}
		log.Printf("[SFU] Negotiation offer sent to %s", c.id)
	}
}

func (c *sfuClient) getLocalTrackFor(remote *webrtc.TrackRemote) *webrtc.TrackLocalStaticRTP {
	return c.localTracks[remote.ID()]
}

func decodeSDP(val interface{}) webrtc.SessionDescription {
	if sdpMap, ok := val.(map[string]interface{}); ok {
		return webrtc.SessionDescription{
			Type: webrtc.NewSDPType(sdpMap["type"].(string)),
			SDP:  sdpMap["sdp"].(string),
		}
	}
	return webrtc.SessionDescription{}
}

func decodeICE(val interface{}) webrtc.ICECandidateInit {
	if iceMap, ok := val.(map[string]interface{}); ok {
		c := webrtc.ICECandidateInit{}
		if s, ok := iceMap["candidate"].(string); ok {
			c.Candidate = s
		}
		if s, ok := iceMap["sdpMid"].(string); ok {
			c.SDPMid = &s
		}
		if n, ok := iceMap["sdpMLineIndex"].(float64); ok {
			u := uint16(n)
			c.SDPMLineIndex = &u
		}
		return c
	}
	return webrtc.ICECandidateInit{}
}

// ICE restart logic (for failed ICE)
func sfuRestartICE(client *sfuClient) {
	client.restartMu.Lock()
	defer client.restartMu.Unlock()
	if client.hasFailedICE {
		return // only one restart at a time
	}
	client.hasFailedICE = true
	defer func() { client.hasFailedICE = false }()

	if client.pc.SignalingState() != webrtc.SignalingStateStable {
		log.Printf("[SFU] ICE restart: PC not stable for %s, skipping", client.id)
		return
	}

	log.Printf("[SFU] Restarting ICE for %s", client.id)
	offer, err := client.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		log.Printf("[SFU] ICE restart CreateOffer: %v", err)
		return
	}
	if err := client.pc.SetLocalDescription(offer); err != nil {
		log.Printf("[SFU] ICE restart SetLocalDescription: %v", err)
		return
	}
	client.send <- map[string]interface{}{
		"type":  "offer",
		"offer": client.pc.LocalDescription(),
	}
	log.Printf("[SFU] ICE-restart offer sent to %s", client.id)
}

// Random SFU ID
func randomSFUID() string {
	return fmt.Sprintf("sfu-%d", rand.Intn(100000))
}
