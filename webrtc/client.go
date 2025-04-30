package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"gocv.io/x/gocv"
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
	//xmlFile := flag.String("cascade", "haarcascade_frontalface_default.xml", "path to Haar cascade XML")
	file := flag.String("file", "", "path to a local video file to stream instead of webcam")
	room := flag.String("room", "default", "room name")
	id := flag.String("id", "", "your unique client ID")
	flag.Parse()

	if *id == "" {
		*id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	myID := *id
	log.Printf("My ID: %s", myID)

	// --- 2) Prepare static‐RTP tracks ---
	m := webrtc.MediaEngine{}

	// Only H264 @ PT 96
	m.RegisterCodec(
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   90000,
				SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f",
			},
			PayloadType: 109,
		},
		webrtc.RTPCodecTypeVideo,
	)

	// Only Opus @ PT 97
	m.RegisterCodec(
		webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeOpus,
				ClockRate: 48000,
				Channels:  2,
			},
			PayloadType: 111,
		},
		webrtc.RTPCodecTypeAudio,
	)

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

	// --- 3) Connect to signaling server ---
	wsURL := fmt.Sprintf("%s?room=%s", *server, *room)
	headers := http.Header{
		"Origin": []string{"https://noremac.dev"},
	}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		log.Fatalf("WebSocket dial error: %v", err)
	}
	defer ws.Close()

	// send our join message
	ws.WriteJSON(map[string]interface{}{
		"join": myID, "from": myID, "room": *room, "type": "join",
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

	// decide which ffmpeg launcher to use:
	if *file != "" {
		go runFFmpegFileWithDetection(
			*file,
			"haarcascade_frontalface_default.xml",
			640, 480, 30,
			"rtp://127.0.0.1:5004",
			map[string]string{
				"c:v":          "libx264",
				"preset":       "ultrafast",
				"tune":         "zerolatency",
				"an":           "",
				"f":            "rtp",
				"payload_type": "109",
			},
		)
		// Audio‐only RTP + SDP
		go runFFmpegFileCLI(
			*file,
			"rtp://127.0.0.1:5006",
			map[string]string{
				"y":            "",          // overwrite output file
				"map":          "0:a",       // pick only the audio stream
				"c:a":          "libopus",   // encode audio
				"payload_type": "111",       // audio payload type
				"f":            "rtp",       // RTP muxer
				"sdp_file":     "audio.sdp", // write out audio.sdp
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

// runFFmpegFileWithDetection opens inputFile, runs Haar-cascade face detection,
// draws rectangles, and pipes the annotated frames into FFmpeg which sends RTP to output.
func runFFmpegFileWithDetection(
	inputFile string, // path to video file
	cascadeXML string, // e.g. "haarcascade_frontalface_default.xml"
	width, height, fps int,
	output string, // e.g. "rtp://127.0.0.1:5004"
	outArgs map[string]string,
) {
	// 1) Load classifier
	classifier := gocv.NewCascadeClassifier()
	defer classifier.Close()
	if !classifier.Load(cascadeXML) {
		log.Fatalf("Error loading cascade file: %s", cascadeXML)
	}

	// 2) Open video file
	vc, err := gocv.VideoCaptureFile(inputFile)
	if err != nil {
		log.Fatalf("Error opening video file: %v", err)
	}
	defer vc.Close()

	// 3) Prepare mat and rectangle color
	img := gocv.NewMat()
	defer img.Close()
	rectColor := color.RGBA{G: 255, A: 0}

	// 4) Build FFmpeg command to read rawvideo from stdin
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-f", "rawvideo", "-pix_fmt", "bgr24",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", fmt.Sprint(fps),
		"-i", "pipe:0",
	}
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

	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatalf("Error getting stdin pipe for ffmpeg: %v", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start ffmpeg: %v", err)
	}

	// 5) Read frames, detect, draw, and write into FFmpeg
	for {
		if ok := vc.Read(&img); !ok || img.Empty() {
			break // end of file
		}
		// optionally resize if source isn't the exact width/height
		if img.Cols() != width || img.Rows() != height {
			gocv.Resize(img, &img, image.Pt(width, height), 0, 0, gocv.InterpolationDefault)
		}
		// face detection
		rects := classifier.DetectMultiScale(img)
		for _, r := range rects {
			gocv.Rectangle(&img, r, rectColor, 3)
		}
		// write raw BGR bytes to ffmpeg
		if _, err := stdin.Write(img.ToBytes()); err != nil {
			log.Printf("Error writing frame to ffmpeg: %v", err)
			break
		}
	}

	stdin.Close()
	cmd.Wait()
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

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		log.Printf("🔗 ICE state with %s: %s", peerID, s.String())
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("🏓 PeerConnection state with %s: %s", peerID, s.String())
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
