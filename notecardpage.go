package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"robot-webrtc/deps"
	"strings"
)

type Page struct {
	Id          string
	RoomID      string
	Title       string
	TextEntries []string
	Images      []string
}

func registerPageRoutes(mux *http.ServeMux, registry *CommandRegistry, deps *deps.Deps) {
	pageDb := deps.Docs.WithCollection("pages")

	mux.HandleFunc("/page/{id...}", func(w http.ResponseWriter, r *http.Request) {
		pageId := r.FormValue("pageId")

		switch r.Method {
		case http.MethodGet:
			// Display page
			servePageThreadPage(w, r, deps)

		case http.MethodPut:
			// Show edit form
			fmt.Println("Editing page:", pageId)
			var page Page
			if err := pageDb.Get(pageId, &page); err != nil {
				http.Error(w, fmt.Sprintf("Page not found: %v", err), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(EditPageDetailPage(page).Render()))

		case http.MethodPatch:
			// Handle the submitted edits (text + image uploads)
			
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				http.Error(w, "invalid form data", http.StatusBadRequest)
				return
			}

			// Load existing page
			var page Page
			if err := pageDb.Get(pageId, &page); err != nil {
				http.Error(w, fmt.Sprintf("Page not found: %v", err), http.StatusNotFound)
				return
			}

			// Reset slices
			page.TextEntries = nil
			page.Images = nil

			// 1) Collect all text entries
			for key, vals := range r.MultipartForm.Value {
				if strings.HasPrefix(key, "TextEntry") && len(vals) > 0 {
					page.TextEntries = append(page.TextEntries, vals[0])
				}
			}

			// 2) Process any image uploads
			for key, fhs := range r.MultipartForm.File {
				if strings.HasPrefix(key, "Image") && len(fhs) > 0 {
					fh := fhs[0]
					file, err := fh.Open()
					if err != nil {
						log.Printf("Failed to open upload %s: %v", key, err)
						continue
					}
					defer file.Close()

					// Ensure storage dir exists
					if err := os.MkdirAll("pages", 0755); err != nil {
						http.Error(w, "could not save image", http.StatusInternalServerError)
						return
					}

					// Determine extension
					ext := filepath.Ext(fh.Filename)
					if ext == "" {
						ext = ".png"
					}
					// Name file: <pageId>-<key>.ext
					filename := fmt.Sprintf("%s-%s%s", pageId, key, ext)
					dstPath := filepath.Join("pages", filename)

					out, err := os.Create(dstPath)
					if err != nil {
						http.Error(w, "could not save image", http.StatusInternalServerError)
						return
					}
					defer out.Close()

					if _, err := io.Copy(out, file); err != nil {
						http.Error(w, "could not write image", http.StatusInternalServerError)
						return
					}

					// Public URL for the new image
					page.Images = append(page.Images, "/pages/"+filename)
				}
			}

			// 3) Persist changes
			if err := pageDb.Set(pageId, page); err != nil {
				log.Printf("Could not save page %s: %v", pageId, err)
				http.Error(w, "could not save page", http.StatusInternalServerError)
				return
			}

			// 4) Render updated PageDetailPage
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(PageDetailPage(page).Render()))

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func servePageThreadPage(w http.ResponseWriter, r *http.Request, deps *deps.Deps) {
	pageId := r.FormValue("pageId")
	roomId := r.FormValue("roomId")

	cards, err := loadCards()
	if err != nil {
		fmt.Println("Could not load cards:", err)
		return
	}
	card, err := getCard(pageId, cards)
	if err != nil {
		fmt.Println("Card not found:", err)
		return
	}

	pageDb := deps.Docs.WithCollection("pages")

	var page Page
	err = pageDb.Get(pageId, &page)
	if err != nil {
		fmt.Println(fmt.Sprintf("Could not load page %s: %v. Creating new page.", pageId, err))
		page = Page{
			Id:     pageId,
			RoomID: roomId,
			Title:  "New Page",
			TextEntries: []string{
				card.ShortEntry,
				card.AIEntry,
			},
			Images: []string{
				card.ImageURL,
			},
		}
		err = pageDb.Set(pageId, page)
		if err != nil {
			fmt.Println(fmt.Sprintf("Could not save new page %s: %v", pageId, err))
			return
		}
		dblist, _ := pageDb.List()
		for _, f := range dblist {
			fmt.Println(fmt.Sprintf("Page in DB: %s", f.ID))
		}
		var page Page
		pageDb.Get(pageId, &page)

		fmt.Println(fmt.Sprintf("Created new page %s with title '%s'", pageId, page.Title))
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(PageDetailPage(page).Render()))
}

func PageDetailPage(p Page) *Node {
	return Div(
		Id("main-content"),
		Class("grid w-full h-screen overflow-auto bg-base-100 p-8"),

		Div(
			Form(
				Class("space-y-4 m-4"),
				Attr("hx-put", fmt.Sprintf("/page/%s", p.Id)),
				Attr("hx-target", "#main-content"),
				Attr("hx-swap", "outerHTML"),
				Input(
					Type("hidden"),
					Name("roomId"),
					Value(p.RoomID),
				),
				Input(
					Type("hidden"),
					Name("pageId"),
					Value(p.Id),
				),
				Input(
					Type(("submit")),
					Class("btn btn-ghost bg-neutral-400 text-xl"),
					Value("Edit"),
				),
			),
		),
		// Title
		Div(H1(Class("text-4xl font-extrabold mb-6"), T(p.Title))),
		// Header image (if present)
		Ch(func() []*Node {
			imgs := make([]*Node, 0, len(p.Images))
			for _, img := range p.Images {
				imgs = append(imgs, Div(
					Class("place-self-center"),
					Img(
						Attr("src", img),
						Attr("alt", "Page header image"),
						Class("w-full  object-cover rounded-lg mb-6"),
					)))
			}
			return imgs
		}()),

		Ch(func() []*Node {
			entries := make([]*Node, 0, len(p.TextEntries))
			for i, entry := range p.TextEntries {
				entries = append(entries, Div(
					Class("mb-4 p-4 bg-gray-100 rounded-lg shadow-md"),
					H2(Class("text-xl font-semibold mb-2"), T(fmt.Sprintf("Entry %d", i+1))),
					P(Class("text-gray-700"), T(entry)),
				))
			}
			return entries
		}()),
	)
}

func EditPageDetailPage(p Page) *Node {
	return Div(
		Id("main-content"),
		Class("w-full h-screen overflow-auto bg-base-100 p-8"),

		Form(
			Class("space-y-6"),
			Attr("hx-patch", "/page/"+p.Id),
			Attr("hx-target", "#main-content"),
			Attr("hx-swap", "outerHTML"),
			Input(Type("hidden"), Name("roomId"), Value(p.RoomID)),
			Input(Type("hidden"), Name("pageId"), Value(p.Id)),

			// Title (read-only)
			Div(
				Class("flex items-center space-x-4"),
				H1(Class("text-3xl font-bold flex-grow"), T(p.Title)),
			),

			Div(
				Class("space-y-4"),
				H2(Class("text-xl font-semibold"), T("Text Entries")),

				// entries container
				Div(
					Id("text-entries-container"),
					Ch(func() []*Node {
						var hidden []*Node
						for i, imgURL := range p.Images {
							hidden = append(hidden, Div(
								Img(
									Src(imgURL),
									Class("w-full  object-cover rounded-lg mb-6"),
								),
								Input(
									Type("file"),
									Name(fmt.Sprintf("Image%d", i+1)),
									Class("file-input file-input-bordered w-full"),
									Value(imgURL),
								)),
							)
						}
						return hidden
					}()),
					Ch(func() []*Node {
						var nodes []*Node
						for i, entry := range p.TextEntries {
							idx := i + 1
							nodes = append(nodes,
								Div(
									Class("space-y-1"),
									Label(Class("block font-medium"), T(fmt.Sprintf("Entry %d", idx))),
									TextArea(
										Class("textarea textarea-bordered w-full h-32"),
										Name(fmt.Sprintf("TextEntry%d", idx)),
										T(entry),
									),
								),
							)
						}
						if len(nodes) == 0 {
							nodes = []*Node{
								Div(
									Class("space-y-1"),
									Label(Class("block font-medium"), T("Entry1")),
									TextArea(
										Class("textarea textarea-bordered w-full h-32"),
										Name("TextEntry1"),
									),
								),
							}
						}
						return nodes
					}()),
				),

				// Add Entry button
				Button(
					Type("button"),
					Id("add-entry-btn"),
					Class("btn btn-outline"),
					T("Add Entry"),
				),
			),

			// Save button
			Div(
				Class("flex justify-end"),
				Input(
					Type("submit"),
					Class("btn btn-primary"),
					Value("Save Page"),
				),
			),

			// JS to handle adding new text entries
			Script(Raw(`
                document.getElementById('add-entry-btn').addEventListener('click', function() {
                    const container = document.getElementById('text-entries-container');
                    const count = container.querySelectorAll('textarea').length + 1;
                    const wrapper = document.createElement('div');
                    wrapper.className = 'space-y-1';
                    const label = document.createElement('label');
                    label.className = 'block font-medium';
                    label.innerText = 'Entry ' + count;
                    const ta = document.createElement('textarea');
                    ta.className = 'textarea textarea-bordered w-full h-32';
                    ta.name = 'TextEntry' + count;
                    wrapper.appendChild(label);
                    wrapper.appendChild(ta);
                    container.appendChild(wrapper);
                });
            `)),
		),
	)
}
