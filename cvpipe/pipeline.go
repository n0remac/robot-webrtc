// cvpipe/pipeline.go
package cvpipe

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"gocv.io/x/gocv"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type BoxesEvent struct {
	PubID    string `json:"pubId"`
	TrackID  string `json:"trackId"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	TsUnixMs int64  `json:"ts"`
	Boxes    []Box  `json:"boxes"`
}

type Pipeline struct {
	Key       string // pubID|trackID
	W, H, FPS int

	// GStreamer processes
	DecCmd *exec.Cmd
	DecOut io.ReadCloser

	EncCmd *exec.Cmd
	EncIn  io.WriteCloser

	// UDP RTP reader from the encoder payloader
	RTPListen net.PacketConn

	cancel context.CancelFunc
	wg     sync.WaitGroup

	In       chan *rtp.Packet
	mu       sync.RWMutex
	inClosed bool

	// Will be set to the SDP-negotiated payload type after AddTrack()
	OutPT uint8

	subsMu sync.RWMutex
	subs   map[chan *rtp.Packet]struct{}

	InRTPConn net.Conn

	FirstRawFrame chan struct{}

	Boxes chan BoxesEvent
}

type Config struct {
	Key         string // pubID|trackID
	CodecCap    webrtc.RTPCodecCapability
	W, H, FPS   int
	CodecPT     uint8  // incoming H264 PT (from publisher)
	OutPT       uint8  // negotiated PT for processed stream (we will override dynamically)
	OutTrackID  string // origTrackID + "-proc"
	OutStreamID string // pubID (recommended)
	OutRTPPort  int    // UDP port for RTP out (localhost)
	H264Bitrate string // e.g. "2500k"

	InRTPPort int   // UDP port for RTP IN (publisher → decoder)
	InPT      uint8 // publisher's H264 payload type (for udpsrc caps)

	PubID   string // for logging
	TrackID string // for logging
}

func StartH264(ctx context.Context, cfg Config) (*Pipeline, error) {
	ctx, cancel := context.WithCancel(ctx)

	// ---------- 1) GStreamer decoder: RTP(H264) → raw BGR on stdout ----------
	dec := exec.CommandContext(ctx, "gst-launch-1.0",
		"-q",
		"udpsrc", "address=127.0.0.1",
		fmt.Sprintf("port=%d", cfg.InRTPPort),
		fmt.Sprintf("caps=application/x-rtp,media=video,clock-rate=90000,encoding-name=H264,packetization-mode=1,payload=%d", cfg.InPT),
		"!", "rtpjitterbuffer", "latency=200", // (no drop-on-late)
		"!", "rtph264depay",
		"!", "h264parse", "config-interval=1", "disable-passthrough=true",
		"!", "avdec_h264", "max-threads=1",
		"!", "queue", "leaky=downstream", "max-size-buffers=0", "max-size-time=0", "max-size-bytes=0",
		"!", "videoconvert",
		"!", "videoscale",
		"!", fmt.Sprintf("video/x-raw,format=BGR,width=%d,height=%d", cfg.W, cfg.H),
		"!", "fdsink", "fd=1",
	)

	decOut, err := dec.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("decoder stdout: %w", err)
	}
	dec.Stderr = os.Stderr

	// ---------- 2) GStreamer encoder: raw BGR on stdin → RTP(H264) → UDP ----------
	enc := exec.CommandContext(ctx, "gst-launch-1.0",
		"-q",
		"fdsrc", "fd=0", "do-timestamp=true",
		"!",
		"videoparse",
		"format=bgr",
		fmt.Sprintf("width=%d", cfg.W),
		fmt.Sprintf("height=%d", cfg.H),
		fmt.Sprintf("framerate=%d/1", cfg.FPS),
		"!",
		"videoconvert",
		"!",
		"x264enc",
		"tune=zerolatency", "speed-preset=ultrafast",
		"key-int-max=30", "bframes=0", "cabac=false",
		"byte-stream=true", "rc-lookahead=0", "aud=true", "ref=1",
		fmt.Sprintf("bitrate=%d", kbpsFrom(cfg.H264Bitrate)),
		"!",
		"h264parse", "config-interval=1", // (no au-delimiter here)
		"!",
		"rtph264pay", "pt=96", "config-interval=1", "mtu=1200",
		"!",
		"queue", "leaky=downstream", "max-size-buffers=0", "max-size-time=0", "max-size-bytes=0",
		"!",
		"udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", cfg.OutRTPPort),
		"sync=false", "async=false",
	)

	encIn, err := enc.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("encoder stdin: %w", err)
	}
	enc.Stderr = os.Stderr

	// ---------- 3) UDP sockets ----------
	// (a) where we WRITE publisher RTP to feed the decoder
	decSink, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", cfg.InRTPPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial decoder udp: %w", err)
	}
	// (b) where we READ encoder RTP payloads to forward to subscribers
	outRTP, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", cfg.OutRTPPort))
	if err != nil {
		cancel()
		_ = decSink.Close()
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	pl := &Pipeline{
		Key:           cfg.Key,
		W:             cfg.W,
		H:             cfg.H,
		FPS:           cfg.FPS,
		DecCmd:        dec,
		DecOut:        decOut,
		EncCmd:        enc,
		EncIn:         encIn,
		InRTPConn:     decSink, // used by your OnTrack loop (Write(b))
		RTPListen:     outRTP,  // read encoder RTP here
		cancel:        cancel,
		subs:          make(map[chan *rtp.Packet]struct{}),
		FirstRawFrame: make(chan struct{}),
		Boxes:         make(chan BoxesEvent, 32),
	}

	// keep gst debug if you like
	dec.Env = append(os.Environ(), "GST_DEBUG=2")
	enc.Env = append(os.Environ(), "GST_DEBUG=2")

	// ---------- start processes ----------
	if err := dec.Start(); err != nil {
		pl.Stop()
		return nil, fmt.Errorf("start decoder: %w", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		lp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", cfg.InRTPPort))
		if err != nil {
			// gst already bound the port -> ready
			break
		}
		_ = lp.Close()
		if time.Now().After(deadline) {
			log.Printf("[CV] WARN: decoder udp %d not bound yet; proceeding", cfg.InRTPPort)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := enc.Start(); err != nil {
		pl.Stop()
		return nil, fmt.Errorf("start encoder: %w", err)
	}

	// ---------- 4) Read decoder raw BGR → CV (gray/clahe/detect) → write to encoder stdin ----------
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()
		defer pl.EncIn.Close()

		reader := bufio.NewReader(pl.DecOut)
		frameBytes := cfg.W * cfg.H * 3
		buf := make([]byte, frameBytes)

		// --- Haar cascade ---
		classifier := gocv.NewCascadeClassifier()
		defer classifier.Close()
		loaded := classifier.Load("haarcascade_frontalface_default.xml")
		if !loaded {
			log.Printf("[CV] WARNING: could not load Haarcascade; proceeding without detection")
		}

		// --- Working Mats (reused per frame) ---
		gray := gocv.NewMat()
		defer gray.Close()
		small := gocv.NewMat()
		defer small.Close()

		// (optional) if you want to draw boxes on the output video
		boxColor := color.RGBA{0, 255, 0, 255}

		// CLAHE to improve local contrast for face detection
		clahe := gocv.NewCLAHEWithParams(2.0, image.Pt(8, 8)) // clipLimit=2.0, tileGrid=8x8
		defer clahe.Close()

		// Downscale factor for detector to reduce CPU (tune as needed)
		const detScale = 0.5
		minDetSize := image.Pt(30, 30) // minimum face size at detection scale

		firstFrame := true
		goodFrames := 0
		framesSec := 0
		bytesSec := 0
		tick := time.Now()

		for {
			if _, err := io.ReadFull(reader, buf); err != nil {
				if err == io.EOF {
					log.Printf("[CV] decoder EOF")
				} else {
					log.Printf("[CV] decoder read error: %v", err)
				}
				return
			}
			if firstFrame {
				log.Printf("[CV] decoder produced first raw frame (W=%d H=%d bytes=%d)", cfg.W, cfg.H, frameBytes)
				firstFrame = false
			}
			goodFrames++
			if goodFrames == 3 { // after 3 decoded frames, consider "stable enough"
				select {
				case <-pl.FirstRawFrame:
					// already closed
				default:
					close(pl.FirstRawFrame)
				}
			}
			framesSec++
			bytesSec += frameBytes
			if time.Since(tick) >= time.Second {
				// log.Printf("[CV] decoder raw frames in last 1s: %d (W=%d H=%d); wrote-to-enc bytes=%.2f MiB",
				// 	framesSec, cfg.W, cfg.H, float64(bytesSec)/(1024*1024))
				framesSec, bytesSec = 0, 0
				tick = time.Now()
			}

			// Convert bytes → BGR Mat
			mat, err := bytesToMatBGR(buf, pl.W, pl.H)
			if err != nil {
				log.Printf("[CV] bytesToMatBGR failed: %v", err)
				return
			}

			// --- Preprocess for Haar: BGR -> Gray -> Denoise -> Contrast enhance (CLAHE) ---
			gocv.CvtColor(mat, &gray, gocv.ColorBGRToGray)
			// Light blur to reduce noise (helps cascades)
			gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)
			// CLAHE enhances local contrast (faces pop more in varied lighting)
			clahe.Apply(gray, &gray)

			// --- Downscale for faster detection ---
			if detScale != 1.0 {
				w := int(float64(pl.W) * detScale)
				h := int(float64(pl.H) * detScale)
				gocv.Resize(gray, &small, image.Pt(w, h), 0, 0, gocv.InterpolationArea)
			} else {
				gray.CopyTo(&small)
			}

			// --- Detect faces ---
			var rects []image.Rectangle
			if loaded {
				// tune params if needed:
				// scaleFactor=1.1, minNeighbors=5, flags=0, minSize=30x30 (at detScale)
				rects = classifier.DetectMultiScaleWithParams(
					small,
					1.1, 5, 0,
					minDetSize, image.Pt(0, 0),
				)
			}

			log.Printf("[CV] detected %d faces", len(rects))

			// --- Rescale rects back to full resolution ---
			if len(rects) > 0 && detScale != 1.0 {
				inv := 1.0 / detScale
				for i := range rects {
					r := rects[i]
					rects[i] = image.Rect(
						int(float64(r.Min.X)*inv),
						int(float64(r.Min.Y)*inv),
						int(float64(r.Max.X)*inv),
						int(float64(r.Max.Y)*inv),
					)
				}
			}

			// --- OPTIONAL: draw boxes onto the outgoing video (remove if you only want metadata) ---
			for _, r := range rects {
				gocv.Rectangle(&mat, r, boxColor, 3)
			}

			// --- OPTIONAL: emit metadata (if your Pipeline has a Boxes channel) ---
			if pl.Boxes != nil && (loaded && len(rects) > 0) {
				boxes := make([]Box, 0, len(rects))
				for _, r := range rects {
					boxes = append(boxes, Box{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()})
				}
				select {
				case pl.Boxes <- BoxesEvent{
					PubID:    cfg.PubID,
					TrackID:  cfg.TrackID,
					W:        pl.W,
					H:        pl.H,
					TsUnixMs: time.Now().UnixMilli(),
					Boxes:    boxes,
				}:
				default:
					// drop if the channel is full to avoid blocking the media thread
				}
			}

			// --- Ensure encoder still gets 3-channel BGR ---
			if _, err := pl.EncIn.Write(mat.ToBytes()); err != nil {
				log.Printf("[CV] enc stdin write failed: %v", err)
				mat.Close()
				return
			}
			mat.Close()
		}
	}()

	// ---------- 5) Read RTP from encoder → broadcast to subscribers ----------
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()
		defer pl.RTPListen.Close()

		buf := make([]byte, 1500)
		var pkt rtp.Packet
		count := 0
		last := time.Now()

		first := true

		for {
			n, _, err := pl.RTPListen.ReadFrom(buf)
			if err != nil {
				return
			}
			if err := pkt.Unmarshal(buf[:n]); err != nil {
				continue
			}
			if first {
				log.Printf("[CV] encoder → first RTP: ssrc=%d pt=%d seq=%d ts=%d",
					pkt.SSRC, pkt.PayloadType, pkt.SequenceNumber, pkt.Timestamp)
				first = false
			}
			pl.broadcast(&pkt)
			count++
			if time.Since(last) > 2*time.Second {
				// log.Printf("[CV] enc→RTP packets in last 2s: %d", count)
				count = 0
				last = time.Now()
			}
		}
	}()

	return pl, nil
}

func (p *Pipeline) Stop() {
	p.cancel()
	if p.InRTPConn != nil {
		_ = p.InRTPConn.Close()
	}
	_ = p.RTPListen.Close()

	if p.EncIn != nil {
		_ = p.EncIn.Close()
	}
	if p.DecCmd != nil {
		_ = p.DecCmd.Wait()
	}
	if p.EncCmd != nil {
		_ = p.EncCmd.Wait()
	}
	p.wg.Wait()

	p.subsMu.Lock()
	for ch := range p.subs {
		close(ch)
	}
	p.subs = make(map[chan *rtp.Packet]struct{})
	p.subsMu.Unlock()
}

func (p *Pipeline) Push(pkt *rtp.Packet) {
	if p == nil || pkt == nil {
		return
	}
	p.mu.RLock()
	ch, closed := p.In, p.inClosed
	p.mu.RUnlock()
	if closed || ch == nil {
		return
	}
	select {
	case ch <- pkt:
	default:
		// drop to keep realtime
	}
}

func (p *Pipeline) Subscribe() <-chan *rtp.Packet {
	ch := make(chan *rtp.Packet, 256)
	p.subsMu.Lock()
	p.subs[ch] = struct{}{}
	p.subsMu.Unlock()
	return ch
}

func (p *Pipeline) Unsubscribe(ch chan *rtp.Packet) {
	p.subsMu.Lock()
	if _, ok := p.subs[ch]; ok {
		delete(p.subs, ch)
		close(ch)
	}
	p.subsMu.Unlock()
}

func (p *Pipeline) broadcast(pkt *rtp.Packet) {
	p.subsMu.RLock()
	for c := range p.subs {
		// copy so each subscriber can mutate PayloadType/etc safely
		cp := *pkt
		select {
		case c <- &cp:
		default:
			// drop to keep realtime
		}
	}
	p.subsMu.RUnlock()
}

/* ---------- helpers ---------- */

func ensureAnnexB(b []byte) []byte {
	if bytes.Contains(b, []byte{0x00, 0x00, 0x00, 0x01}) || bytes.Contains(b, []byte{0x00, 0x00, 0x01}) {
		return b
	}
	return avcToAnnexB(b)
}

func avcToAnnexB(avc []byte) []byte {
	out := make([]byte, 0, len(avc)+1024)
	r := bytes.NewReader(avc)
	for r.Len() > 4 {
		var n uint32
		_ = readUint32(r, &n)
		if n == 0 || int(n) > r.Len() {
			break
		}
		out = append(out, 0x00, 0x00, 0x00, 0x01)
		chunk := make([]byte, n)
		_, _ = io.ReadFull(r, chunk)
		out = append(out, chunk...)
	}
	return out
}

func readUint32(r *bytes.Reader, v *uint32) error {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	*v = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return nil
}

func bytesToMatBGR(b []byte, w, h int) (gocv.Mat, error) {
	mat, err := gocv.NewMatFromBytes(h, w, gocv.MatTypeCV8UC3, b)
	return mat, err
}

func kbpsFrom(s string) int {
	// Accept forms like "2500k", "1500K", or plain "2500"
	if len(s) == 0 {
		return 2500
	}
	var n int
	if _, err := fmt.Sscanf(s, "%dk", &n); err == nil {
		return n
	}
	if _, err := fmt.Sscanf(s, "%dK", &n); err == nil {
		return n
	}
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	return 2500
}
