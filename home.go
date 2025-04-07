package main

import "fmt"

func HomePage(websocketRegistry *CommandRegistry) *Node {
	websocketRegistry.RegisterWebsocket(("home"), func(command string, hub *Hub, data map[string]interface{}) {
		fmt.Println("Home command received:", command)
		fmt.Println("Data:", data)
	})

	id := "home"

	return DefaultLayout(
		Attr("hx-ext", "ws"),
		Attr("ws-connect", "/websocket?room="+id),
		Div(Attrs(map[string]string{
			"class":      "flex flex-col items-center min-h-screen",
			"data-theme": "dark",
		}),
			NavBar(),
			Div(
				Class("p-8 text-center"),
				H1(Class("text-3xl font-bold"), T("Welcome to My Portfolio Site")),
				P(Class("mt-4"),
					T("This site contains a collection of my personal projects, including ShadowReddit and a Children's Book Generator."),
				),
				P(Class("mt-2"),
					T("Feel free to explore the above links to see the individual apps!"),
				),
			),
			Form(
				Attr("ws-send", "submit"),
				Input(
					Name("home"),
				),
				Input(
					Type("submit"),
					Value("Submit"),
				),
			),
		),
	)
}

// NavBar returns a centered navigation bar with dark theme styling.
func NavBar() *Node {
	return Nav(Class("bg-base-300 p-4 w-full"),
		Div(Class("container mx-auto flex justify-center"),
			Ul(Class("flex space-x-6"),
				Li(A(Href("/shadowreddit"), T("ShadowReddit"))),
				Li(A(Href("/story"), T("Story Generator"))),
				Li(A(Href("/video"), T("Video Conference"))),
			),
		),
	)
}
