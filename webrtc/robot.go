package webrtc

import (
	"net/http"

	. "github.com/n0remac/robot-webrtc/html"
)

func RobotControlHandler(w http.ResponseWriter, r *http.Request) {
	page := DefaultLayout(
		Style(Raw(LoadFile("webrtc/video.css"))),
		Script(Raw(LoadFile("webrtc/logger.js"))),
		Script(Raw(LoadFile("webrtc/robot-control.js"))),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center justify-center min-h-screen bg-black",
			"data-theme": "dark",
		}),
			// Video area
			Div(
				Id("video-area"), Class("mt-12 flex flex-col items-center space-y-4"),
				Button(
					Id("start-video-btn"),
					Class("mb-4 px-6 py-2 bg-green-600 text-white rounded shadow hover:bg-green-700 transition"),
					T("Start Video"),
				),
				Video(
					Id("robot-video"),
					Class("w-[640px] h-[480px] bg-[#111] rounded-lg border-2 border-[#333]"),
					Attr("autoplay", ""),
					Attr("playsinline", ""),
				),
			),
			// Controls legend and feedback
			Div(
				Id("controls-legend"), Class("mt-8 flex flex-col items-center text-gray-300"),
				T("Use your keyboard to control the robot:"),
				Ul(Class("mt-2 space-y-1"),
					Li(T("W/A/S/D - Move")),
					Li(T("T/F/G/H - Move Claw")),
					Li(T("R/Y - Open/Close Claw")),
					Li(T("I/J/K/L, - Move Camera")),
				),
				Div(Id("control-status"), Class("mt-4 text-green-400 text-lg")),
			),
			Div(
				Id("mobile-log"),
				Class("fixed bottom-0 left-0 right-0 max-h-[30vh] overflow-y-auto bg-black bg-opacity-80 text-green-300 text-xs p-2 font-mono z-50"),
			),
		),
	)

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(page.Render()))
}
