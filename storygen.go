package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sashabaranov/go-openai"
)

// ------------------------------------------------------
// Data Structures & Book JSON model
// ------------------------------------------------------

type Book struct {
	Title string          `json:"title"`
	Pages map[string]Page `json:"pages"`
}

type Page struct {
	Text             string `json:"text"`
	ImageDescription string `json:"image_description"`
	ImagePath        string `json:"image_path"` // Where we store the local path to the PNG
}

// Some models might return pages as an array; handle both
type PagesArray []Page

func tryUnmarshalPages(data []byte) (map[string]Page, error) {
	obj := make(map[string]Page)
	if err := json.Unmarshal(data, &obj); err == nil {
		return obj, nil
	}
	var arr PagesArray
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("pages is not an object nor an array")
	}
	obj = make(map[string]Page)
	for i, p := range arr {
		obj[strconv.Itoa(i+1)] = p
	}
	return obj, nil
}

// A session tracks one generation process
type Session struct {
	ID          string
	Title       string
	TotalPages  int
	CurrentPage int
	Error       error
	Done        bool

	Mutex sync.Mutex
	Conn  *websocket.Conn
}

// ------------------------------------------------------
// Session store
// ------------------------------------------------------

var (
	sessions      = make(map[string]*Session)
	sessionsMutex sync.Mutex
)

// ------------------------------------------------------
// Function definition for OpenAI "function calling"
// ------------------------------------------------------

var createBookFn = openai.FunctionDefinition{
	Name:        "create_book_json",
	Description: "Generate a JSON structure for a children's-style book with a title, where each page has text and an image description",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "The title of the book",
			},
			"pages": map[string]interface{}{
				"type":        "object",
				"description": "An object with numeric keys. Each key has {text, image_description}",
				"patternProperties": map[string]interface{}{
					"^[0-9]+$": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"text": map[string]interface{}{
								"type": "string",
							},
							"image_description": map[string]interface{}{
								"type": "string",
							},
						},
						"required": []string{"text", "image_description"},
					},
				},
			},
		},
		"required": []string{"title", "pages"},
	},
}

// ------------------------------------------------------
// Main Entrypoint: GenerateStory()
// ------------------------------------------------------

func GenerateStory() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY not set")
	}
	client := openai.NewClient(apiKey)

	// Create directory for final books
	if err := os.MkdirAll("books", 0755); err != nil {
		log.Fatalf("Cannot create 'books' directory: %v", err)
	}

	// GET /story -> prompt page
	http.HandleFunc("/story", func(w http.ResponseWriter, r *http.Request) {
		// Prepare random prompts
		randomPrompts := []string{
			"A curious Python exploring the magical land of code.",
			"A JavaScript knight on a quest to debug a dragon.",
			"The adventure of Sir Recursion in the Looping Kingdom.",
			"A brave coder who learns the secret of inheritance.",
			"A little function that calls itself and grows into a recursion master.",
			"The tale of the missing semicolon in the Kingdom of Syntax.",
			"A tiny variable on a journey to find its scope.",
			"The debugging adventures of Captain Compile in Code Land.",
			"A lonely array that discovers the beauty of a slice.",
			"The secret life of a function that loves closures.",
			"A mischievous pointer exploring the enchanted world of C++.",
			"A Java class learning about polymorphism in a whimsical realm.",
			"An epic quest of a code snippet to find the lost library.",
			"A mysterious bug in the enchanted forest of code.",
			"A journey through the infinite loops of recursion.",
			"A curious algorithm solving puzzles in Data Structure Land.",
			"The adventure of a Boolean variable searching for truth in a binary world.",
			"A playful code block that learns to scope its dreams.",
			"A function that meets an object and discovers the magic of methods.",
			"The magical debug log that reveals hidden secrets of code.",
			"A coder's adventure in the realm of machine learning.",
			"The tale of a server that dreams of becoming a microservice.",
			"The story of a data packet traveling through a network forest.",
			"A journey to the center of a binary tree.",
			"A brave little app that transforms into a full-stack hero.",
			"The secret world of code comments and the messages they hide.",
			"An epic tale of endless loops and the break that saved the day.",
			"A magical land where syntax errors turn into friendly puzzles.",
			"A coder's quest to master Git in a version control wonderland.",
			"The adventure of a compiler that speaks in riddles.",
			"A quest to find the elusive null pointer in a sea of variables.",
			"The enchanting story of a class finding its supertype.",
			"The debugging diary of a programmer battling system crashes.",
			"A whimsical journey through the realm of regular expressions.",
			"An epic battle between functions and methods in an object-oriented kingdom.",
			"A mysterious algorithm that turns chaos into order.",
			"The story of a code editor that writes its own tale.",
			"A little module that grows into a powerful framework hero.",
			"The secret life of a sandboxed process inside a virtual machine.",
			"A story of microservices that learn to communicate in harmony.",
			"A code snippet that dreams of becoming an open source library.",
			"The enchanted castle of a collaborative open source project.",
			"A developer's journey from 'Hello, World!' to a complex application.",
			"The tale of a software patch that saved an entire system.",
			"A whimsical dance of data types in the memory ballroom.",
			"An adventurous bootcamp graduate exploring the startup jungle.",
			"The mystery of the disappearing semicolon in the Code Chronicles.",
			"A journey into the depths of a stack overflow, emerging wiser.",
			"The legend of the immortal loop that finally meets a break.",
			"A playful saga of binary trees, where each node tells its own story.",
		}

		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(randomPrompts), func(i, j int) {
			randomPrompts[i], randomPrompts[j] = randomPrompts[j], randomPrompts[i]
		})
		defaultPrompt := randomPrompts[0]
		randomPromptsForDisplay := randomPrompts[1:4]

		ServeNode(BookPromptPage(defaultPrompt, randomPromptsForDisplay))(w, r)
	})

	// POST /story/generate -> create a new session & start generation
	http.HandleFunc("/story/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		userPrompt := r.FormValue("userPrompt")
		if userPrompt == "" {
			http.Error(w, "No prompt provided", http.StatusBadRequest)
			return
		}
		sessionID := randomID()
		session := &Session{ID: sessionID}
		sessionsMutex.Lock()
		sessions[sessionID] = session
		sessionsMutex.Unlock()

		log.Printf("[INFO] Created session=%s for prompt=%q", sessionID, userPrompt)
		go generateBook(session, userPrompt, client)

		// Redirect to loading page
		http.Redirect(w, r, "/story/loading?id="+sessionID, http.StatusSeeOther)
	})

	// GET /story/loading?id=XYZ -> the "loading" page with WebSocket progress
	http.HandleFunc("/story/loading", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		ServeNode(BookLoadingPage(sessionID))(w, r)
	})

	// GET /story/ws?id=XYZ -> WebSocket for progress updates
	http.HandleFunc("/story/ws", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		sessionsMutex.Lock()
		session, ok := sessions[sessionID]
		sessionsMutex.Unlock()
		if !ok {
			http.Error(w, "Invalid session ID", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ERROR] Upgrading to WS: %v", err)
			return
		}
		log.Printf("[INFO] WebSocket connection established for session=%s", sessionID)

		session.Mutex.Lock()
		session.Conn = conn
		// If generation is already done, push final update
		if session.Done {
			if session.Error != nil {
				sendWebSocketError(session, session.Error)
				conn.Close()
			} else {
				sendWebSocketDone(session)
				conn.Close()
			}
		}
		session.Mutex.Unlock()
	})

	// GET /story/view?id=XYZ -> dynamic rendering from JSON
	http.HandleFunc("/story/view", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("id")
		if sessionID == "" {
			http.Error(w, "Missing session ID", http.StatusBadRequest)
			return
		}
		// The JSON file is at books/<sessionID>/book.json
		bookPath := fmt.Sprintf("books/%s/book.json", sessionID)

		// Read the book and render
		ServeNode(BookViewPage(bookPath, sessionID))(w, r)
	})

	// Serve the static /books/... files (images, JSON, etc.)
	http.Handle("/books/", http.StripPrefix("/books/", http.FileServer(http.Dir("books"))))

	log.Println("[INFO] Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ------------------------------------------------------
// The Prompt Page (uses DefaultLayout)
// ------------------------------------------------------

func BookPromptPage(defaultPrompt string, randomPrompts []string) *Node {
	// Show a form + some random prompts
	var promptItems []*Node
	for _, rp := range randomPrompts {
		promptItems = append(promptItems, Li(T(rp)))
	}

	return DefaultLayout(
		Div(Class("max-w-2xl mx-auto p-8 space-y-6"),
			H1(Class("text-2xl font-bold"), T("Generate a Custom Children's Book")),
			P(T("Enter a brief idea or theme for your story, or try one of the random prompts below.")),

			Form(Method("POST"), Action("/story/generate"),
				Div(Class("mb-4"),
					Label(For("userPrompt"), T("Your Book Idea:")),
					TextArea(Id("userPrompt"), Name("userPrompt"), Class("border rounded w-full p-2"), Rows(4),
						T(defaultPrompt),
					),
					Button(Type("submit"), Class("bg-blue-500 hover:bg-blue-700 text-white font-bold py-2 px-4 rounded mt-2"),
						T("Generate Book"),
					),
				),
			),

			H2(T("Random Prompt Ideas")),
			Ul(Ch(promptItems)), // Insert the <li> items
			P(T("(Copy/paste one of these into the text area if you like!)")),
		),
	)
}

// ------------------------------------------------------
// The "loading" page with WebSocket script
// ------------------------------------------------------

func BookLoadingPage(sessionID string) *Node {
	scriptJS := fmt.Sprintf(`
	let protocol = (window.location.protocol === "https:") ? "wss://" : "ws://";
	let ws = new WebSocket(protocol + window.location.host + "/story/ws?id=%s");
	let progressEl = document.getElementById("progressArea");

	ws.onopen = function() {
	  console.log("WebSocket connected for session=%s");
	};

	ws.onmessage = function(event) {
	  let data = JSON.parse(event.data);
	  if (data.type === "progress") {
	    let html = "<div>Title: " + data.title + "</div>" +
	               "<div>Generating page " + data.currentPage + " of " + data.totalPages + "</div>";
	    progressEl.innerHTML = html;
	  } else if (data.type === "done") {
		// Data contains: data.url = "/story/view?id=<sessionID>"
		window.location.href = data.url;
		ws.close();
	  } else if (data.type === "error") {
	    progressEl.innerHTML = "<p style='color:red'>Error: " + data.error + "</p>";
	    ws.close();
	  }
	};

	ws.onerror = function() {
	  progressEl.innerHTML = "<p style='color:red'>WebSocket error!</p>";
	};

	ws.onclose = function() {
	  console.log("WebSocket closed for session=%s");
	};
`, sessionID, sessionID, sessionID)

	return DefaultLayout(
		Div(Class("max-w-2xl mx-auto p-8 space-y-4"),
			H1(Class("text-2xl font-bold"), T("Generating your book...")),
			Div(Id("progressArea"), T("Waiting for updates...")),
		),
		Script(Raw(scriptJS)),
	)
}

// ------------------------------------------------------
// Generating the Book (AI logic + saving JSON + images)
// ------------------------------------------------------

func generateBook(session *Session, userPrompt string, client *openai.Client) {
    log.Printf("[INFO] Starting generation for session=%s, prompt=%q", session.ID, userPrompt)

    // 1) Create ChatCompletion with function-calling
    systemMsg := openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleSystem,
        Content: "You are a creative children's book generator. Return your answer in JSON by calling create_book_json with a whimsical style.",
    }
    userMsg := openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleUser,
        Content: userPrompt,
    }
    resp, err := client.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
        Model:        "gpt-4-0613",
        Messages:     []openai.ChatCompletionMessage{systemMsg, userMsg},
        Functions:    []openai.FunctionDefinition{createBookFn},
        FunctionCall: openai.FunctionCall{Name: createBookFn.Name},
    })
    if err != nil {
        sendWebSocketError(session, fmt.Errorf("OpenAI error: %v", err))
        return
    }
    if len(resp.Choices) == 0 || resp.Choices[0].Message.FunctionCall == nil {
        sendWebSocketError(session, fmt.Errorf("No function call response from model"))
        return
    }
    fnCall := resp.Choices[0].Message.FunctionCall

    // 2) Parse the JSON into our Book struct
    var raw map[string]json.RawMessage
    if err := json.Unmarshal([]byte(fnCall.Arguments), &raw); err != nil {
        sendWebSocketError(session, fmt.Errorf("Error parsing JSON arguments: %v", err))
        return
    }
    var title string
    if err := json.Unmarshal(raw["title"], &title); err != nil {
        sendWebSocketError(session, fmt.Errorf("Error unmarshaling title: %v", err))
        return
    }
    pages, err := tryUnmarshalPages(raw["pages"])
    if err != nil {
        sendWebSocketError(session, fmt.Errorf("Error unmarshaling pages: %v", err))
        return
    }
    book := Book{Title: title, Pages: pages}

    // If no pages, finalize
    if len(book.Pages) == 0 {
        finalizeEmptyBook(session, book)
        sendWebSocketDone(session)
        return
    }

    // Create subdir "books/<sessionID>"
    subdir := fmt.Sprintf("books/%s", session.ID)
    if err := os.MkdirAll(subdir, 0755); err != nil {
        sendWebSocketError(session, fmt.Errorf("cannot create subdir: %v", err))
        return
    }

    // Sort numeric page keys
    var pageNums []int
    for k := range pages {
        if n, e := strconv.Atoi(k); e == nil {
            pageNums = append(pageNums, n)
        }
    }
    sort.Ints(pageNums)

    // concurrency: create an image for each page in parallel
    var wg sync.WaitGroup
    wg.Add(len(pageNums))

    var mu sync.Mutex          // guards updates to book.Pages
    pagesCompleted := 0        // how many pages are done
    session.Mutex.Lock()
    session.Title = book.Title // for progress
    session.TotalPages = len(pageNums)
    session.CurrentPage = 0
    session.Mutex.Unlock()

    for i, pgNum := range pageNums {
        go func(i, pgNum int) {
            defer wg.Done()

            // If we already encountered an error, skip
            session.Mutex.Lock()
            alreadyDone := session.Done || session.Error != nil
            session.Mutex.Unlock()
            if alreadyDone {
                return
            }

            strPage := strconv.Itoa(pgNum)
            pg := book.Pages[strPage]

            // Request a 512x512 image from OpenAI
            imgResp, err := client.CreateImage(context.Background(), openai.ImageRequest{
                Prompt: fmt.Sprintf("Children's book style illustration: %s. No text in the image.", pg.ImageDescription),
                N:      1,
                Size:   "512x512",
            })
            if err != nil {
                sendWebSocketError(session, fmt.Errorf("image generation error on page %s: %v", strPage, err))
                return
            }
            if len(imgResp.Data) == 0 {
                sendWebSocketError(session, fmt.Errorf("no image data returned for page %s", strPage))
                return
            }

            // Download the image
            imageURL := imgResp.Data[0].URL
            res, err := http.Get(imageURL)
            if err != nil {
                sendWebSocketError(session, fmt.Errorf("error downloading image for page %s: %v", strPage, err))
                return
            }
            imgBytes, err := io.ReadAll(res.Body)
            res.Body.Close()
            if err != nil {
                sendWebSocketError(session, fmt.Errorf("error reading image for page %s: %v", strPage, err))
                return
            }

            // Save image file: "page-<i+1>.png"
            imageFilename := fmt.Sprintf("page-%d.png", i+1)
            fullImagePath := fmt.Sprintf("%s/%s", subdir, imageFilename)
            if err := os.WriteFile(fullImagePath, imgBytes, 0644); err != nil {
                sendWebSocketError(session, fmt.Errorf("error writing image file: %v", err))
                return
            }

            // Update the Book data (ImagePath)
            mu.Lock()
            pg.ImagePath = imageFilename
            book.Pages[strPage] = pg
            mu.Unlock()

            // Increment "pagesCompleted", send progress
            session.Mutex.Lock()
            pagesCompleted++
            session.CurrentPage = pagesCompleted
            sendWebSocketProgress(session)
            session.Mutex.Unlock()

        }(i, pgNum)
    }

    wg.Wait()

    // If an error occurred, session.Error is set -> just return
    session.Mutex.Lock()
    defer session.Mutex.Unlock()
    if session.Error != nil || session.Done {
        // Already marked as done or error
        return
    }

    // Save final JSON
    bookPath := fmt.Sprintf("%s/book.json", subdir)
    if err := saveBookJSON(bookPath, book); err != nil {
        sendWebSocketError(session, err)
        return
    }

    session.Done = true
    sendWebSocketDone(session)
}

// finalize an empty book (zero pages)
func finalizeEmptyBook(session *Session, book Book) {
	// Just store an empty book in books/<sessionID>/book.json
	subdir := fmt.Sprintf("books/%s", session.ID)
	if err := os.MkdirAll(subdir, 0755); err != nil {
		sendWebSocketError(session, fmt.Errorf("cannot create subdir for empty book: %v", err))
		return
	}
	bookPath := fmt.Sprintf("%s/book.json", subdir)
	if err := saveBookJSON(bookPath, book); err != nil {
		sendWebSocketError(session, err)
		return
	}

	session.Mutex.Lock()
	session.Done = true
	session.Mutex.Unlock()
}

// ------------------------------------------------------
// Rendering the Book from JSON (dynamic view)
// ------------------------------------------------------

// BookViewPage: loads the JSON from disk, then builds GoDom nodes
func BookViewPage(jsonPath string, sessionID string) *Node {
	// Attempt to read JSON
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return DefaultLayout(
			H1(Class("text-2xl font-bold text-red-600"), T("Error loading book.")),
			P(T(fmt.Sprintf("Could not read file: %v", err))),
		)
	}
	var book Book
	if err := json.Unmarshal(data, &book); err != nil {
		return DefaultLayout(
			H1(Class("text-2xl font-bold text-red-600"), T("Error parsing JSON.")),
			P(T(fmt.Sprintf("Could not unmarshal: %v", err))),
		)
	}

	// If no pages, show "Empty"
	if len(book.Pages) == 0 {
		return DefaultLayout(
			H1(Class("text-2xl font-bold"), T(book.Title)),
			P(T("No pages were generated.")),
		)
	}

	// Sort the pages by numeric key
	var pageNums []int
	for k := range book.Pages {
		if n, err := strconv.Atoi(k); err == nil {
			pageNums = append(pageNums, n)
		}
	}
	sort.Ints(pageNums)

	// Build the page nodes
	var pageDivs []*Node

	// Title "page"
	titlePage := Div(Id("title-page"),
		H1(Class("text-3xl font-bold my-4"), T(book.Title)),
		P(T("Enjoy your custom story!")),
	)
	pageDivs = append(pageDivs, titlePage)

	for i, pageNum := range pageNums {
		pg := book.Pages[strconv.Itoa(pageNum)]
		pageDivID := fmt.Sprintf("page-%d", i+1)

		// The image path is something like "page-1.png"
		// but we need to reference it from the outside as /books/<sessionID>/page-1.png
		imageURL := fmt.Sprintf("/books/%s/%s", sessionID, pg.ImagePath)

		thisPageDiv := Div(Class("book-page hidden"), Id(pageDivID),
			P(T(pg.Text)),
			Img(Src_(imageURL), Alt("Page Illustration"), Class("mx-auto my-4")),
		)
		pageDivs = append(pageDivs, thisPageDiv)
	}

	// Navigation
	navButtons := Div(Id("nav-buttons"), Class("my-4"),
		Button(Type("button"), Class("mx-2 px-4 py-2 bg-gray-200 rounded"), OnClick("prevPage()"), T("Previous Page")),
		Button(Type("button"), Class("mx-2 px-4 py-2 bg-gray-200 rounded"), OnClick("nextPage()"), T("Next Page")),
	)

	// Script for showing/hiding pages
	scriptRaw := fmt.Sprintf(`
	var currentPage = 0;
	var totalPages = %d;

	function showPage(index) {
	  // Hide the old page if not the title
	  if (currentPage === 0) {
	    document.getElementById("title-page").style.display = "none";
	  } else {
	    var oldPage = document.getElementById("page-" + currentPage);
	    if (oldPage) {
	      oldPage.classList.add("hidden");
	    }
	  }
	  currentPage = index;
	  if (currentPage === 0) {
	    document.getElementById("title-page").style.display = "block";
	    return;
	  }
	  var newPage = document.getElementById("page-" + currentPage);
	  if (newPage) {
	    newPage.classList.remove("hidden");
	  }
	}

	function nextPage() {
	  if (currentPage < totalPages) {
	    showPage(currentPage + 1);
	  }
	}

	function prevPage() {
	  if (currentPage > 0) {
	    showPage(currentPage - 1);
	  }
	}

	window.onload = function() {
	  // Title page is shown by default, pages hidden
	  document.getElementById("title-page").style.display = "block";
	};
`, len(pageNums))

	return DefaultLayout(
		Style(T(`
			body {
			  font-family: sans-serif;
			  text-align: center;
			  background: #FAF9F6;
			  margin: 0; padding: 0;
			}
			.book-page {
			  width: 80%;
			  margin: 1em auto;
			  max-width: 600px;
			  padding: 1em;
			  background: white;
			  border: 1px solid #DDD;
			  box-shadow: 0 2px 5px rgba(0,0,0,0.2);
			}
		`)),
		Div(Class("container mx-auto p-4"),
			Ch(pageDivs),
			navButtons,
		),
		Script(Raw(scriptRaw)),
	)
}

// ------------------------------------------------------
// JSON Saving
// ------------------------------------------------------

func saveBookJSON(path string, b Book) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

// ------------------------------------------------------
// Random ID + WebSocket Helpers
// ------------------------------------------------------

func randomID() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 12)
	rand.Seed(time.Now().UnixNano())
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func sendWebSocketProgress(s *Session) {
	if s.Conn == nil {
		return
	}
	msg := map[string]interface{}{
		"type":        "progress",
		"title":       s.Title,
		"currentPage": s.CurrentPage,
		"totalPages":  s.TotalPages,
	}
	if err := s.Conn.WriteJSON(msg); err != nil {
		log.Printf("[WARN] session=%s sending progress error: %v", s.ID, err)
		s.Conn.Close()
		s.Conn = nil
	}
}

func sendWebSocketDone(s *Session) {
    if s.Conn == nil {
        return
    }
    // We'll just call it "url" for clarity
    msg := map[string]interface{}{
        "type": "done",
        "url":  fmt.Sprintf("/story/view?id=%s", s.ID),
    }
    if err := s.Conn.WriteJSON(msg); err != nil {
        log.Printf("[WARN] session=%s sending done error: %v", s.ID, err)
        s.Conn.Close()
        s.Conn = nil
    }
}


func sendWebSocketError(s *Session, err error) {
	s.Mutex.Lock()
	s.Error = err
	s.Done = true
	s.Mutex.Unlock()

	if s.Conn != nil {
		msg := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}
		_ = s.Conn.WriteJSON(msg)
		s.Conn.Close()
		s.Conn = nil
	}
	log.Printf("[ERROR] session=%s -> %v", s.ID, err)
}
