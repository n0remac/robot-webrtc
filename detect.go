package main

import (
	"fmt"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

func Detect() {
	// Path to Haar cascade XML file
	xmlFile := "haarcascade_frontalface_default.xml"
	if len(os.Args) > 1 {
		xmlFile = os.Args[1]
	}

	// Load classifier
	classifier := gocv.NewCascadeClassifier()
	defer classifier.Close()
	if !classifier.Load(xmlFile) {
		fmt.Printf("Error loading cascade file: %s\n", xmlFile)
		return
	}

	// Open default webcam
	webcam, err := gocv.OpenVideoCapture(0)
	if err != nil {
		fmt.Printf("Error opening video capture device: %v\n", err)
		return
	}
	defer webcam.Close()

	// Create window to display results
	window := gocv.NewWindow("Face Detection")
	defer window.Close()

	// Prepare image matrix
	img := gocv.NewMat()
	defer img.Close()

	// Rectangle color for detected faces
	rectColor := color.RGBA{G: 255, A: 0}

	fmt.Println("Starting face detection. Press any key in the window to quit.")

	// Read frames in a loop
	for {
		if ok := webcam.Read(&img); !ok || img.Empty() {
			continue
		}

		// Detect faces
		rects := classifier.DetectMultiScale(img)

		// Draw rectangles around faces
		for _, r := range rects {
			gocv.Rectangle(&img, r, rectColor, 3)
		}

		// Show the image in the window
		window.IMShow(img)

		// Break the loop on any key press
		if window.WaitKey(1) >= 0 {
			break
		}
	}
}
