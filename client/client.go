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
	"github.com/stianeikeland/go-rpio/v4"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/devices/v3/pca9685"
	"periph.io/x/host/v3"
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
	// 1) Init Periph
	if _, err := host.Init(); err != nil {
		log.Fatal("host.Init:", err)
	}

	// 2) Open I²C bus #1
	bus, err := i2creg.Open("1")
	if err != nil {
		log.Fatal("i2creg.Open:", err)
	}
	defer bus.Close()

	// 3) Software reset the PCA9685 (General Call 0x06)
	if err := bus.Tx(0x00, []byte{0x06}, nil); err != nil {
		log.Println("PCA9685 SWRST failed:", err)
	}
	time.Sleep(10 * time.Millisecond)

	// 4) Now create & configure the driver
	pca, err := pca9685.NewI2C(bus, pca9685.I2CAddr)
	if err != nil {
		log.Fatal("pca9685.NewI2C:", err)
	}
	if err := pca.SetPwmFreq(50 * physic.Hertz); err != nil {
		log.Fatal("SetPwmFreq:", err)
	}
	if err := pca.SetAllPwm(0, 0); err != nil {
		log.Fatal("SetAllPwm:", err)
	}
	servos := pca9685.NewServoGroup(pca, 50, 650, 0, 180)

	if err := rpio.Open(); err != nil {
		fmt.Println("Unable to open GPIO:", err)
		return
	}
	defer rpio.Close()

	m1 := NewMotor("MOTOR1", 1)
	m2 := NewMotor("MOTOR2", 1)
	m3 := NewMotor("MOTOR3", 1)
	m4 := NewMotor("MOTOR4", 1)

	motors := []*Motor{m1, m2, m3, m4}

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

	// pump RTP
	go pumpRTP("[::]:5004", videoTrack, 109)
	go pumpRTP("[::]:5006", audioTrack, 111)

	// handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// connect and maintain signalling
	go func() {
		for {
			if err := connectAndSignal(api, myID, *room, *server, motors, servos); err != nil {
				log.Printf("Signal loop exited with: %v; retrying in 1s...", err)
			}
			time.Sleep(time.Second)
		}
	}()

	// start FFmpeg push
	go runFFmpegCLI(
		"/dev/video0", "v4l2", 30, "640x480",
		"rtp://127.0.0.1:5004",
		map[string]string{"vf": "hflip,vflip", "c:v": "libx264", "preset": "ultrafast", "tune": "zerolatency", "pix_fmt": "yuv420p", "an": "", "f": "rtp", "payload_type": "109"},
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
	motors []*Motor,
	servos *pca9685.ServoGroup,
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
		peersMu.Lock()
		defer peersMu.Unlock()
		if pc := peers[from]; pc != nil {
			return pc
		}
		pc := createPeerConnection(api, myID, from, room, ws)
		peers[from] = pc
		return pc
	}

	switch typ {
	case "join":
		log.Printf("Peer %s joined → creating PC + DC offer", from)
		pc := getOrCreatePC()

		// --- NEW: actively create a data-channel so Go side always offers it ---
		if dc, err := pc.CreateDataChannel("keyboard", nil); err != nil {
			log.Printf("CreateDataChannel keyboard error: %v", err)
		} else {
			dc.OnOpen(func() {
				log.Printf("✔︎ Go DataChannel 'keyboard' open")
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				m1 := motors[0]
				m2 := motors[1]
				m3 := motors[2]
				m4 := motors[3]

				log.Printf("Received message on DataChannel 'keyboard': %s", string(msg.Data))

				type Msg struct {
					Key    string
					Action string
				}
				var message Msg
				if err := json.Unmarshal(msg.Data, &message); err != nil {
					log.Printf("Error unmarshalling message: %v", err)
					return
				}

				log.Printf("Received action: %s", message.Action)
				log.Printf("Received key: %s", message.Key)

				const speed = 60 // degrees per second

				// helper to kick off or stop a move
				act := func(pin, dir int) {
					if message.Action == "pressed" {
						if err := Move(servos, pin, dir, speed); err != nil {
							log.Printf("Move error pin %d: %v", pin, err)
						}
					} else {
						Stop(pin)
					}
				}

				switch string(message.Key) {
				// Claw (pin 4): r=open, f=close
				case "r":
					act(4, +1)
				case "f":
					act(4, -1)

				// Up/Down (pin 6): t=up, g=down
				case "t":
					act(6, +1)
				case "g":
					act(6, -1)

				// Left/Right (pin 5): y=right, d=left
				case "y":
					act(5, +1)
				case "h":
					act(5, -1)

				// Camera tilt (pin 14): i=up, k=down
				case "i":
					act(14, +1)
				case "k":
					act(14, -1)

				// Camera pan (pin 15): l=right, j=left
				case "l":
					act(15, +1)
				case "j":
					act(15, -1)

				case "1":
					if message.Action == "pressed" {
						log.Println("1 key pressed")
						m1.Forward(100)
					} else if message.Action == "released" {
						log.Println("1 key released")
						m1.Stop()
					}
				case "2":
					if message.Action == "pressed" {
						log.Println("2 key pressed")
						m2.Forward(100)
					} else if message.Action == "released" {
						log.Println("2 key released")
						m2.Stop()
					}
				case "3":
					if message.Action == "pressed" {
						log.Println("3 key pressed")
						m3.Forward(100)
					} else if message.Action == "released" {
						log.Println("3 key released")
						m3.Stop()
					}
				case "4":
					if message.Action == "pressed" {
						log.Println("4 key pressed")
						m4.Forward(100)
					} else if message.Action == "released" {
						log.Println("4 key released")
						m4.Stop()
					}
				case "w":
					if message.Action == "pressed" {
						log.Println("w key pressed")
						m1.Reverse(100)
						m3.Forward(100)

						m2.Reverse(100)
						m4.Forward(100)
					} else if message.Action == "released" {
						log.Println("w key released")
						m1.Stop()
						m3.Stop()
						m2.Stop()
						m4.Stop()
					}
				case "s":
					log.Println("Backward command received")
					if message.Action == "pressed" {
						log.Println("s key pressed")
						m1.Forward(100)
						m3.Reverse(100)

						m2.Forward(100)
						m4.Reverse(100)
					} else if message.Action == "released" {
						log.Println("s key released")
						m1.Stop()
						m3.Stop()
						m2.Stop()
						m4.Stop()
					}
				case "a":
					log.Println("a key pressed")
					if message.Action == "pressed" {
						log.Println("a key pressed")
						m1.Forward(100)
						m3.Reverse(100)

						m2.Reverse(100)
						m4.Forward(100)
					} else if message.Action == "released" {
						log.Println("a key released")
						m1.Stop()
						m3.Stop()
						m2.Stop()
						m4.Stop()
					}
				case "d":
					log.Println("d key pressed")
					if message.Action == "pressed" {
						log.Println("d key pressed")
						m1.Reverse(100)
						m3.Forward(100)

						m2.Forward(100)
						m4.Reverse(100)

					} else if message.Action == "released" {
						log.Println("d key released")
						m1.Stop()
						m3.Stop()
						m2.Stop()
						m4.Stop()
					}
				}
			})
		}
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
		peersMu.Lock()
		pc := peers[from]
		peersMu.Unlock()
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
		raw := msg["candidate"].(map[string]interface{})
		ice := webrtc.ICECandidateInit{
			Candidate:     raw["candidate"].(string),
			SDPMid:        ptrString(raw["sdpMid"].(string)),
			SDPMLineIndex: ptrUint16(uint16(raw["sdpMLineIndex"].(float64))),
		}

		peersMu.Lock()
		pc := peers[from]
		peersMu.Unlock()
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
		peersMu.Lock()
		pc := peers[from]
		delete(peers, from)
		peersMu.Unlock()
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
) *webrtc.PeerConnection {
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: globalIceServers,
	})
	if err != nil {
		log.Fatalf("NewPeerConnection error: %v", err)
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
		makingOfferMu.Lock()
		makingOffer[peerID] = true
		makingOfferMu.Unlock()

		offer, err := pc.CreateOffer(nil)
		if err != nil {
			log.Printf("OnNegotiationNeeded CreateOffer: %v", err)
			return
		}
		if err := pc.SetLocalDescription(offer); err != nil {
			log.Printf("OnNegotiationNeeded SetLocalDescription: %v", err)
			return
		}
		ws.WriteJSON(map[string]interface{}{
			"type":  "offer",
			"offer": pc.LocalDescription(),
			"from":  myID,
			"to":    peerID,
			"room":  room,
			"name":  "robot",
		})

		// clear the flag once sent
		makingOfferMu.Lock()
		makingOffer[peerID] = false
		makingOfferMu.Unlock()
	})

	// register ICE-candidate and connection handlers
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		ws.WriteJSON(map[string]interface{}{
			"type":      "candidate",
			"candidate": c.ToJSON(),
			"from":      myID,
			"to":        peerID,
			"room":      room,
			"name":      "robot",
		})
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateFailed {
			restartICE(pc, ws, myID, peerID, room)
		}
	})

	// add tracks _after_ OnNegotiationNeeded is set
	if _, err := pc.AddTrack(videoTrack); err != nil {
		log.Fatalf("AddTrack video: %v", err)
	}
	if _, err := pc.AddTrack(audioTrack); err != nil {
		log.Fatalf("AddTrack audio: %v", err)
	}

	return pc
}

// helpers for pointers
func ptrString(s string) *string { return &s }
func ptrUint16(u uint16) *uint16 { return &u }

func restartICE(pc *webrtc.PeerConnection, ws *websocket.Conn, myID, peerID, room string) {
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
	ws.WriteJSON(map[string]interface{}{
		"type":  "offer",
		"offer": pc.LocalDescription(),
		"from":  myID,
		"to":    peerID,
		"room":  room,
		"name":  "robot",
	})
	log.Printf("▶ ICE-restart sent to %s", peerID)
	makingOffer[peerID] = false
}

// connectAndSignal manages WebSocket signalling (with auto-reconnect)
func connectAndSignal(api *webrtc.API, myID, room, wsURL string, motors []*Motor, servos *pca9685.ServoGroup) error {
	// dial
	ws, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("%s?room=%s", wsURL, room), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	// send join
	ws.WriteJSON(map[string]interface{}{"type": "join", "join": myID, "from": myID, "room": room, "name": "robot"})

	// read loop
	for {
		var msg map[string]interface{}
		if err := ws.ReadJSON(&msg); err != nil {
			return err
		}
		handleSignal(ws, api, myID, room, msg, motors, servos)
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
