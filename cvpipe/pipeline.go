// cvproc/pipeline.go
package cvproc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image/color"
	"io"
	"net"
	"os/exec"
	"sync"

	"gocv.io/x/gocv"

	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

type Pipeline struct {
	Key       string // pubID|trackID
	W, H, FPS int
	DecCmd    *exec.Cmd
	DecIn     io.WriteCloser
	DecOut    io.ReadCloser
	EncCmd    *exec.Cmd
	EncIn     io.WriteCloser
	RTPListen net.PacketConn
	TrackOut  *webrtc.TrackLocalStaticRTP

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type Config struct {
	Key         string // pubID|trackID
	W, H, FPS   int
	CodecPT     uint8  // 96/102/etc for H264 payload type (from SDP)
	OutPT       uint8  // payload type used for processed stream RTP (e.g., 96 as well)
	OutTrackID  string // e.g. trackID + "-proc"
	OutStreamID string // e.g. "server-proc"
	OutRTPPort  int    // UDP port to read encoder RTP from (localhost)
	H264Bitrate string // e.g. "2500k"
}

func StartH264(ctx context.Context, remote *webrtc.TrackRemote, api *webrtc.API, cfg Config) (*Pipeline, error) {
	ctx, cancel := context.WithCancel(ctx)

	// 1) Create server-published processed track (same codec as publisher)
	codecCap := remote.Codec().RTPCodecCapability
	procLocal, err := webrtc.NewTrackLocalStaticRTP(codecCap, cfg.OutTrackID, cfg.OutStreamID)
	if err != nil {
		cancel()
		return nil, err
	}

	// 2) ffmpeg decoder: read H264 Annex-B on stdin → raw bgr24 on stdout
	dec := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0",
		"-f", "rawvideo", "-pix_fmt", "bgr24",
		"-vf", fmt.Sprintf("scale=%dx%d,fps=%d", cfg.W, cfg.H, cfg.FPS),
		"pipe:1",
	)
	decIn, _ := dec.StdinPipe()
	decOut, _ := dec.StdoutPipe()

	// 3) ffmpeg encoder: raw bgr24 on stdin → RTP/H264 to localhost:port
	enc := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "bgr24",
		"-s", fmt.Sprintf("%dx%d", cfg.W, cfg.H),
		"-r", fmt.Sprint(cfg.FPS), "-i", "pipe:0",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
		"-g", "60", "-b:v", cfg.H264Bitrate,
		"-f", "rtp",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", cfg.OutRTPPort),
	)
	encIn, _ := enc.StdinPipe()

	// 4) UDP listener to pull encoder RTP and feed TrackLocalStaticRTP
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", cfg.OutRTPPort))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	pl := &Pipeline{
		Key: cfg.Key,
		W:   cfg.W, H: cfg.H, FPS: cfg.FPS,
		DecCmd: dec, DecIn: decIn, DecOut: decOut,
		EncCmd: enc, EncIn: encIn,
		RTPListen: pc,
		TrackOut:  procLocal,
		cancel:    cancel,
	}

	// Start ffmpeg procs
	if err := dec.Start(); err != nil {
		pl.Stop()
		return nil, err
	}
	if err := enc.Start(); err != nil {
		pl.Stop()
		return nil, err
	}

	// 5) Goroutine: read RTP from publisher, depacketize H264 → Annex-B → write to decoder stdin
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()
		defer pl.DecIn.Close()

		// H.264 clock rate is 90kHz
		sb := samplebuilder.New(50, &codecs.H264Packet{}, 90000)

		for {
			pkt, _, readErr := remote.ReadRTP()
			if readErr != nil {
				return
			}
			sb.Push(pkt)

			for {
				samp := sb.Pop()
				if samp == nil {
					break
				}

				// Ensure Annex-B byte stream for ffmpeg’s `-f h264 -i pipe:0`
				annexB := ensureAnnexB(samp.Data) // see helper below
				if len(annexB) == 0 {
					continue
				}
				if _, err := pl.DecIn.Write(annexB); err != nil {
					return
				}
			}
		}
	}()

	// 6) Goroutine: read raw frames (BGR) from decoder → gocv → write processed raw to encoder stdin
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()
		defer pl.EncIn.Close()

		reader := bufio.NewReader(pl.DecOut)
		frameBytes := pl.W * pl.H * 3
		buf := make([]byte, frameBytes)

		// Haar cascade
		classifier := gocv.NewCascadeClassifier()
		defer classifier.Close()
		if ok := classifier.Load("haarcascade_frontalface_default.xml"); !ok {
			fmt.Printf("[CV] failed to load cascade file")
			// You can return here if detection is required:
			// return
		}
		rectColor := color.RGBA{G: 255, A: 0}

		for {
			// Read one raw BGR frame
			if _, err := io.ReadFull(reader, buf); err != nil {
				return
			}

			// Wrap bytes as a Mat (BGR)
			mat, err := bytesToMatBGR(buf, pl.W, pl.H)
			if err != nil {
				return
			}

			// --- OpenCV work ---
			// (Optional) faster detection on grayscale and/or resized image
			// gray := gocv.NewMat()
			// gocv.CvtColor(mat, &gray, gocv.ColorBGRToGray)
			// rects := classifier.DetectMultiScale(gray)
			// gray.Close()

			rects := classifier.DetectMultiScale(mat)
			for _, r := range rects {
				gocv.Rectangle(&mat, r, rectColor, 3) // NOTE: pass *gocv.Mat
			}

			// Write processed frame to encoder
			if _, err := pl.EncIn.Write(mat.ToBytes()); err != nil {
				mat.Close()
				return
			}
			mat.Close()
		}
	}()

	// 7) Goroutine: pull encoder RTP packets from UDP and feed Pion TrackLocalStaticRTP
	pl.wg.Add(1)
	go func() {
		defer pl.wg.Done()
		defer pl.RTPListen.Close()
		buf := make([]byte, 1500)
		for {
			n, _, err := pl.RTPListen.ReadFrom(buf)
			if err != nil {
				return
			}
			_, werr := pl.TrackOut.Write(buf[:n])
			if werr != nil {
				return
			}
		}
	}()

	return pl, nil
}

func ensureAnnexB(b []byte) []byte {
	// If it already contains Annex-B start codes, pass through.
	if bytes.Contains(b, []byte{0x00, 0x00, 0x00, 0x01}) || bytes.Contains(b, []byte{0x00, 0x00, 0x01}) {
		return b
	}
	// Otherwise convert from AVCC (length-prefixed) → Annex-B
	return avcToAnnexB(b)
}

func (p *Pipeline) Stop() {
	p.cancel()
	_ = p.RTPListen.Close()
	if p.DecIn != nil {
		_ = p.DecIn.Close()
	}
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
}

/* ---------- helpers ---------- */

// Convert H264 AVC sample (length-prefixed NALs) → Annex-B (0x00000001 prefix per NAL).
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
