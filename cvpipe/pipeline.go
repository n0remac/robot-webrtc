// cvpipe/pipeline.go
package cvpipe

import (
	"bufio"
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
)

type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`

	Type string `json:"type,omitempty"`
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

	cancel context.CancelFunc
	wg     sync.WaitGroup

	InRTPConn     net.Conn
	FirstRawFrame chan struct{}
	Boxes         chan BoxesEvent
}

type Config struct {
	Key       string // pubID|trackID
	W, H, FPS int

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
		"!",
		"h264parse", "config-interval=1", // (no au-delimiter here)
		"!",
		"rtph264pay", "pt=96", "config-interval=1", "mtu=1200",
		"!",
		"queue", "leaky=downstream", "max-size-buffers=0", "max-size-time=0", "max-size-bytes=0",
		"!",
		"sync=false", "async=false",
	)

	enc.Stderr = os.Stderr

	// ---------- 3) UDP sockets ----------
	// (a) where we WRITE publisher RTP to feed the decoder
	decSink, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", cfg.InRTPPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial decoder udp: %w", err)
	}

	pl := &Pipeline{
		Key:           cfg.Key,
		W:             cfg.W,
		H:             cfg.H,
		FPS:           cfg.FPS,
		DecCmd:        dec,
		DecOut:        decOut,
		InRTPConn:     decSink,
		cancel:        cancel,
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

		reader := bufio.NewReader(pl.DecOut)
		frameBytes := cfg.W * cfg.H * 3
		buf := make([]byte, frameBytes)

		// Haar cascade (faces)
		classifier := gocv.NewCascadeClassifier()
		defer classifier.Close()
		loaded := classifier.Load("haarcascades/fist.xml")
		if !loaded {
			log.Printf("[CV] WARNING: could not load haarcascade_frontalface_default.xml; proceeding without detection")
		}

		// Working Mats
		gray := gocv.NewMat()
		defer gray.Close()
		small := gocv.NewMat()
		defer small.Close()

		// Optional debug draw color
		boxColor := color.RGBA{0, 255, 0, 255}

		// Light preproc helper (same as your original face path)
		clahe := gocv.NewCLAHEWithParams(2.0, image.Pt(8, 8))
		defer clahe.Close()

		// Downscale factor for detector
		const detScale = 0.5
		minDetSize := image.Pt(30, 30) // at detScale

		firstFrame := true
		goodFrames := 0
		framesSec := 0
		bytesSec := 0
		tick := time.Now()

		for {
			// Read raw BGR frame
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
			if goodFrames == 3 {
				select {
				case <-pl.FirstRawFrame:
				default:
					close(pl.FirstRawFrame)
				}
			}
			framesSec++
			bytesSec += frameBytes
			if time.Since(tick) >= time.Second {
				framesSec, bytesSec = 0, 0
				tick = time.Now()
			}

			// bytes → Mat
			mat, err := bytesToMatBGR(buf, pl.W, pl.H)
			if err != nil {
				log.Printf("[CV] bytesToMatBGR failed: %v", err)
				return
			}

			// Preprocess for Haar: BGR -> Gray -> (optional) light denoise -> CLAHE
			gocv.CvtColor(mat, &gray, gocv.ColorBGRToGray)
			gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)
			clahe.Apply(gray, &gray)

			// Downscale
			if detScale != 1.0 {
				w := int(float64(pl.W) * detScale)
				h := int(float64(pl.H) * detScale)
				gocv.Resize(gray, &small, image.Pt(w, h), 0, 0, gocv.InterpolationArea)
			} else {
				gray.CopyTo(&small)
			}

			// Detect faces
			var rects []image.Rectangle
			if loaded {
				rects = classifier.DetectMultiScaleWithParams(
					small,
					1.1, 5, 0,
					minDetSize, image.Pt(0, 0),
				)
			}

			// Rescale to full-res coords
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

			// Optional debug draw (on the preview stream, if you send it)
			for _, r := range rects {
				gocv.Rectangle(&mat, r, boxColor, 3)
			}

			// Emit metadata (Type="face")
			if pl.Boxes != nil && loaded && len(rects) > 0 {
				out := make([]Box, 0, len(rects))
				for _, r := range rects {
					out = append(out, Box{
						X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy(),
						Type: "face",
					})
				}
				select {
				case pl.Boxes <- BoxesEvent{
					PubID:    cfg.PubID,
					TrackID:  cfg.TrackID,
					W:        pl.W,
					H:        pl.H,
					TsUnixMs: time.Now().UnixMilli(),
					Boxes:    out,
				}:
				default:
					// drop if full
				}
			}

			// If you're not piping an annotated video out, just drop the Mat.
			// (If you DO want to forward annotated video, we can wire up a new EncIn in Pipeline.)
			mat.Close()
		}
	}()

	// ---------- 5) Read RTP from encoder → broadcast to subscribers ----------
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()

		var pkt rtp.Packet
		count := 0
		last := time.Now()

		first := true

		for {
			if first {
				log.Printf("[CV] encoder → first RTP: ssrc=%d pt=%d seq=%d ts=%d",
					pkt.SSRC, pkt.PayloadType, pkt.SequenceNumber, pkt.Timestamp)
				first = false
			}
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

	if p.DecCmd != nil {
		_ = p.DecCmd.Wait()
	}
	p.wg.Wait()
}

/* ---------- helpers ---------- */

func bytesToMatBGR(b []byte, w, h int) (gocv.Mat, error) {
	mat, err := gocv.NewMatFromBytes(h, w, gocv.MatTypeCV8UC3, b)
	return mat, err
}

// small helpers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
