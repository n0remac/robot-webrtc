package main


		// go runFFmpegFileWithDetection(
		// 	*file,
		// 	"haarcascade_frontalface_default.xml",
		// 	640, 480, 30,
		// 	"rtp://127.0.0.1:5004",
		// 	map[string]string{
		// 		"c:v":          "libx264",
		// 		"preset":       "ultrafast",
		// 		"tune":         "zerolatency",
		// 		"an":           "",
		// 		"f":            "rtp",
		// 		"payload_type": "109",
		// 	},
		// )
		// // Audio‐only RTP + SDP
		// go runFFmpegFileCLI(
		// 	*file,
		// 	"rtp://127.0.0.1:5006",
		// 	map[string]string{
		// 		"y":            "",          // overwrite output file
		// 		"map":          "0:a",       // pick only the audio stream
		// 		"c:a":          "libopus",   // encode audio
		// 		"payload_type": "111",       // audio payload type
		// 		"f":            "rtp",       // RTP muxer
		// 		"sdp_file":     "audio.sdp", // write out audio.sdp
		// 	},
		// )

// runFFmpegFileWithDetection opens inputFile, runs Haar-cascade face detection,
// draws rectangles, and pipes the annotated frames into FFmpeg which sends RTP to output.
// func runFFmpegFileWithDetection(
// 	inputFile string, // path to video file
// 	cascadeXML string, // e.g. "haarcascade_frontalface_default.xml"
// 	width, height, fps int,
// 	output string, // e.g. "rtp://127.0.0.1:5004"
// 	outArgs map[string]string,
// ) {
// 	// 1) Load classifier
// 	classifier := gocv.NewCascadeClassifier()
// 	defer classifier.Close()
// 	if !classifier.Load(cascadeXML) {
// 		log.Fatalf("Error loading cascade file: %s", cascadeXML)
// 	}

// 	// 2) Open video file
// 	vc, err := gocv.VideoCaptureFile(inputFile)
// 	if err != nil {
// 		log.Fatalf("Error opening video file: %v", err)
// 	}
// 	defer vc.Close()

// 	// 3) Prepare mat and rectangle color
// 	img := gocv.NewMat()
// 	defer img.Close()
// 	rectColor := color.RGBA{G: 255, A: 0}

// 	// 4) Build FFmpeg command to read rawvideo from stdin
// 	args := []string{
// 		"-hide_banner", "-loglevel", "warning",
// 		"-f", "rawvideo", "-pix_fmt", "bgr24",
// 		"-s", fmt.Sprintf("%dx%d", width, height),
// 		"-r", fmt.Sprint(fps),
// 		"-i", "pipe:0",
// 	}
// 	for flag, val := range outArgs {
// 		f := flag
// 		if !strings.HasPrefix(f, "-") {
// 			f = "-" + f
// 		}
// 		args = append(args, f)
// 		if val != "" {
// 			args = append(args, val)
// 		}
// 	}
// 	args = append(args, output)

// 	cmd := exec.Command("ffmpeg", args...)
// 	stdin, err := cmd.StdinPipe()
// 	if err != nil {
// 		log.Fatalf("Error getting stdin pipe for ffmpeg: %v", err)
// 	}
// 	cmd.Stdout = os.Stdout
// 	cmd.Stderr = os.Stderr

// 	if err := cmd.Start(); err != nil {
// 		log.Fatalf("Failed to start ffmpeg: %v", err)
// 	}

// 	// 5) Read frames, detect, draw, and write into FFmpeg
// 	for {
// 		if ok := vc.Read(&img); !ok || img.Empty() {
// 			break // end of file
// 		}
// 		// optionally resize if source isn't the exact width/height
// 		if img.Cols() != width || img.Rows() != height {
// 			gocv.Resize(img, &img, image.Pt(width, height), 0, 0, gocv.InterpolationDefault)
// 		}
// 		// face detection
// 		rects := classifier.DetectMultiScale(img)
// 		for _, r := range rects {
// 			gocv.Rectangle(&img, r, rectColor, 3)
// 		}
// 		// write raw BGR bytes to ffmpeg
// 		if _, err := stdin.Write(img.ToBytes()); err != nil {
// 			log.Printf("Error writing frame to ffmpeg: %v", err)
// 			break
// 		}
// 	}

// 	stdin.Close()
// 	cmd.Wait()
// }