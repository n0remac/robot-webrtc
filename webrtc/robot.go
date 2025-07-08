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
			// controls
			Div(
				Id("control-buttons"),
				Class("mt-8 grid grid-cols-2 gap-8 justify-center items-start"),

				// --- Move Controls ---
				Div(
					Class("flex flex-col items-center"),
					T("Move"),
					Div(
						Class("grid grid-cols-3 gap-2 mb-1"),
						Span(Class("col-span-1"), nil),
						ControlButton("w", "W"),
						Span(Class("col-span-1"), nil),
					),
					Div(
						Class("grid grid-cols-3 gap-2"),
						ControlButton("a", "A"),
						ControlButton("s", "S"),
						ControlButton("d", "D"),
					),
				),

				// --- Claw Controls ---
				Div(
					Class("flex flex-col items-center"),
					T("Claw"),
					Div(
						Class("grid grid-cols-4 gap-2 mb-1"),
						Span(Class("col-span-1"), nil),
						ControlButton("t", "T"),
						Span(Class("col-span-1"), nil),
					),
					Div(
						Class("grid grid-cols-4 gap-2"),
						ControlButton("f", "F"),
						ControlButton("g", "G"),
						ControlButton("h", "H"),
						Span(nil),
					),
				),

				// --- Open/Close Claw ---
				Div(
					Class("flex flex-col items-center"),
					T("Open / Close Claw"),
					Div(
						Class("grid grid-cols-1 gap-2 mb-1"),
						ControlButton("r", "R"),
					),
					Div(
						Class("grid grid-cols-1 gap-2"),
						ControlButton("y", "Y"),
					),
				),

				// --- Camera Controls ---
				Div(
					Class("flex flex-col items-center"),
					T("Camera"),
					Div(
						Class("grid grid-cols-4 gap-2 mb-1"),
						Span(Class("col-span-1"), nil),
						ControlButton("i", "I"),
						Span(Class("col-span-1"), nil),
					),
					Div(
						Class("grid grid-cols-4 gap-2"),
						ControlButton("j", "J"),
						ControlButton("k", "K"),
						ControlButton("l", "L"),
						Span(nil),
					),
				),
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

func ControlButton(key, label string) *Node {
	return Button(
		Class(
			"control-btn "+
				"bg-gray-800 text-gray-200 font-bold px-4 py-2 rounded-lg shadow-md border-b-4 border-gray-900 transition transform duration-75 "+
				"active:translate-y-1 active:shadow-sm active:border-b-2 active:bg-gray-700 "+
				"hover:bg-gray-700 hover:border-b-2 "+
				"select-none",
		),
		Attr("data-key", key),
		T(label),
	)
}
