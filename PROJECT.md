# Robot-WebRTC - Multi-Project Experimentation Platform

## Purpose

This repository serves as a **unified testing ground** for various web application experiments, primarily focused on real-time communication, AI integration, and interactive web experiences. Originally created to house multiple projects in one place for easy testing and deployment, it has evolved into a comprehensive platform combining WebRTC video conferencing, multiplayer games, AI-powered content generation, and robot control systems.

The platform enables rapid prototyping and testing of new web technologies without the overhead of managing multiple separate repositories.

## Architecture

### Monolithic Server with Modular Apps

The application uses a **single Go HTTP server** (`main.go`) that hosts multiple independent web applications, each registered as route handlers:

```
main.go (port 8080)
├── Home                  - Interactive word selection + AI content
├── VideoHandler          - WebRTC conferencing & robot control
├── GameUI               - Multiplayer card game (Trick Evolution)
├── ShadowReddit         - AI-generated Reddit discussions
├── GenerateStory        - Children's book generator
├── Fantasy              - Fantasy card sheet creator
└── Notecard             - Collaborative voting system
```

### Shared Infrastructure

All applications share common infrastructure components:

- **Global WebSocket Hub** (`websocket.WsHub`) - Room-based message routing
- **Command Registry** (`websocket.CommandRegistry`) - Pluggable WebSocket handlers
- **HTML DSL** (`html` package) - Type-safe server-side rendering
- **Database Layer** (`db` package) - SQLite/PostgreSQL abstraction
- **Content Processing** (`content.go`) - AI-powered keyword extraction

### Client Architecture

The application is primarily **server-side rendered** using a custom HTML DSL with dynamic updates via:
- **htmx** for HTML-over-the-wire updates
- **WebSockets** for real-time bidirectional communication
- **WebRTC** for peer-to-peer media streaming
- **Vanilla JavaScript** for specialized interactions (drag-and-drop, media controls)

## Technical Stack

### Backend
- **Language**: Go 1.24
- **WebRTC**: Pion WebRTC v4 (SFU + P2P mesh)
- **WebSocket**: Gorilla WebSocket v1.5
- **AI**: OpenAI GPT integration (go-openai v1.38)
- **Database**: GORM with SQLite/PostgreSQL drivers
- **Search**: Bleve full-text search engine
- **Web Automation**: Chromedp (headless Chrome)
- **Hardware**: Raspberry Pi GPIO (go-rpio, periph.io)
- **Serialization**: Protocol Buffers + gRPC

### Frontend
- **UI Framework**: Tailwind CSS v4.1 + DaisyUI v5.0
- **Dynamic Updates**: htmx
- **Real-time**: WebSocket + WebRTC APIs
- **Build Tools**: PostCSS, Autoprefixer
- **No frontend framework** - Vanilla JS + server-rendered HTML

### Infrastructure
- **TURN/STUN**: Coturn with HMAC-SHA1 time-limited credentials
- **Storage**: File-based JSON for prototyping, SQLite for persistence
- **Logging**: Daily rotating log files (`serverlogs/`)

## Major Components

### 1. WebRTC Module (`webrtc/`)

**Purpose**: Real-time video/audio streaming and robot control

**Features**:
- Dual-mode video conferencing (P2P mesh + SFU)
- Remote robot control with live video feed
- DataChannel for keyboard commands
- Coalesced renegotiation for scalability
- RTCP relay (PLI/FIR) for quality adaptation

**Endpoints**:
- `/video/` - Video conference interface
- `/robot/` - Robot control interface
- `/ws/hub` - P2P signaling
- `/ws/sfu` - SFU signaling

See `webrtc/PROJECT.md` for detailed documentation.

### 2. Trick Evolution Card Game (`cards/`)

**Purpose**: Multiplayer trick-taking card game with customizable rules

**Features**:
- Modular rule engine with hooks (onPlay, onScore, etc.)
- Three card types: Standard, Rule, Phase
- Physical card design tool (printable sheets)
- Real-time multiplayer synchronization
- Drag-and-drop card interface

**Endpoints**:
- `/cards` - Card library
- `/cards/sheet` - Print layout generator
- `/game/` - Game lobby

See `cards/PROJECT.md` for detailed documentation.

### 3. Home/Content System (`home.go`, `content.go`)

**Purpose**: Interactive word selection with AI-generated content

**Features**:
- Click-based word selection from JSON content files
- AI keyword extraction (OpenAI function calling)
- Debounced selection batching (5s timeout)
- WebSocket-driven UI updates
- Content registry with keyword indexing

**How it works**:
1. User clicks words from pre-loaded content
2. Selections accumulate for 5 seconds
3. OpenAI generates content based on selected words
4. New content replaces page via htmx

**Content files**: `content/*.json` (vision, rhythm, decentralization, stack)

### 4. Shadow Reddit (`shadowreddit.go`)

**Purpose**: AI-generated Reddit-style discussions with diverse perspectives

**Features**:
- 20+ distinct comment stances (supportive, opposing, neutral, mixed, narrative)
- Generates original posts + threaded comments
- AI personas with different viewpoints
- Realistic Reddit formatting (bold, italics, quotes)
- WebSocket polling for async generation

**Stances include**:
- Supportive: strong agreement, qualified agreement, empathetic support
- Opposing: direct opposition, moral critique, logical critique
- Neutral: dispassionate analysis, devil's advocate, both sides
- Mixed: it's complicated, everyone at fault, consequentialist view
- Narrative: anecdotes, sarcasm, meta-commentary

### 5. Story Generator (`storygen.go`)

**Purpose**: AI-powered children's book creator with illustrations

**Features**:
- Generates title, pages, text, and image descriptions
- OpenAI DALL-E integration for illustrations
- Real-time generation status updates (WebSocket polling)
- Automatic image download and local storage
- Multiple concurrent book generation support

**Workflow**:
1. User provides story prompt
2. AI generates book structure (JSON)
3. AI creates image descriptions per page
4. DALL-E generates PNG illustrations
5. Book saved to `data/books/{uuid}.json`

### 6. Fantasy Card System (`fantasy.go`)

**Purpose**: Fantasy-themed card sheet generator with print capability

**Features**:
- Card library with visual previews
- Multi-card sheet layout (grid-based)
- Chromedp screenshot generation
- Batch export as ZIP archive
- Print-ready Letter-size layouts

**Endpoints**:
- `/fantasy` - Card index
- `/fantasy/sheet` - Sheet preview
- `/fantasy/screenshot` - Generate PNGs
- `/fantasy/sheet/download` - ZIP download

### 7. Notecard System (`cards/notecard*.go`)

**Purpose**: Collaborative notecard creation with voting

**Features**:
- Short and long text entries
- AI image generation (OpenAI)
- Upvote/downvote system
- Room-based organization
- Persistent JSON storage

**Components**:
- `notecard.go` - Main UI and handlers
- `notecardutils.go` - Data structures and persistence
- `notecardvote.go` - Voting logic

### 8. Robot Control Client (`client/`)

**Purpose**: Raspberry Pi robot with WebRTC streaming

**Hardware Integration**:
- Motor shield control (I2C)
- Servo control (pan/tilt camera, claw)
- Face detection (OpenCV Haar cascades)
- Keyboard-driven control (WASD movement, IJKL camera, RY claw)

**Components**:
- `client.go` - WebRTC peer (connects to server)
- `robot.go` - Hardware abstraction
- `servo.go` - Servo control
- `motorshield.go` - Motor driver interface
- `detection.go` - Face detection

**gRPC Service** (`servo/`):
- Server: Exposes servo control API
- Proto: `servo.proto` defines angle setting methods

## Shared Infrastructure Packages

### `html/` - Custom HTML DSL

**Purpose**: Type-safe, composable HTML generation without templates

**Key Features**:
- Fluent builder API: `Div(Class("container"), Text("Hello"))`
- DaisyUI/Tailwind integration
- Script/style inlining from files
- htmx attribute support
- WebSocket event handlers

**Example**:
```go
Div(
    Class("card bg-base-100"),
    H1(Class("text-2xl"), Text("Title")),
    Button(
        Class("btn btn-primary"),
        HTMX("hx-get", "/api/data"),
        Text("Load")
    )
)
```

### `websocket/` - WebSocket Hub & Registry

**Purpose**: Centralized WebSocket message routing with command pattern

**Key Components**:
- `WsHub` - Global singleton for broadcasting
- `CommandRegistry` - Maps message types to handlers
- Room-based routing (targeted or broadcast)
- Automatic client cleanup on disconnect

**Usage Pattern**:
```go
registry.RegisterWebsocket("playCard", func(data string, hub *Hub, msg map[string]interface{}) {
    // Handle command
    hub.Send("updateHand", response, roomID, playerID)
})
```

### `db/` - Database Abstraction

**Purpose**: Generic repository pattern with SQLite/PostgreSQL support

**Features**:
- GORM-based CRUD operations
- Generic type support (`DBInterface[T]`)
- Bleve full-text search integration
- Upsert/batch operations
- Thread-safe document indexing

**Supported Databases**:
- SQLite (development)
- PostgreSQL (production)

### `servo/` - gRPC Servo Service

**Purpose**: Remote servo control for robot hardware

**API**:
- `SetAngles(ServoAngles)` - Set multiple servo positions
- Protobuf message definitions
- Generated Go code from `.proto`

## Unique Features

### 1. **Unified Multi-App Platform**
Unlike traditional microservices, all applications share a single process, global WebSocket hub, and command registry. This enables:
- Zero-config inter-app communication
- Shared infrastructure (DB, HTML renderer, WebSocket)
- Single deployment artifact
- Easy cross-app feature reuse

### 2. **Custom HTML DSL**
Server-side rendering without templates provides:
- Type safety (compile-time errors)
- IDE autocomplete for HTML structure
- Inline JavaScript/CSS loading
- No template parsing overhead
- Easy composition and reuse

### 3. **Hybrid WebRTC Topology**
Supports both P2P mesh and SFU architectures in a single codebase:
- Automatic mode selection via URL parameter
- Shared signaling infrastructure
- Trade-off between latency and scalability

### 4. **AI-Powered Everywhere**
OpenAI integration across multiple apps:
- Content generation (Home)
- Comment generation (Shadow Reddit)
- Story + illustration creation (Story Generator)
- Image generation (Notecard, Story Generator)
- Keyword extraction (Content system)

### 5. **Command Registry Pattern**
WebSocket messages use a pluggable command system:
- Decoupled message handling
- Type-safe JSON deserialization
- Room-aware routing
- Easy to add new commands without modifying hub

### 6. **Physical Hardware Integration**
Seamless integration of web UI with Raspberry Pi hardware:
- Real-time servo control via gRPC
- WebRTC video streaming from robot
- DataChannel for low-latency commands
- Face detection with visual feedback

### 7. **Print-Ready Card Generators**
Both Trick Evolution and Fantasy systems generate printer-friendly layouts:
- Precise 8.5×11" Letter size
- Grid-based card arrangement
- Chromedp for pixel-perfect screenshots
- Batch export for deck printing

## File Structure

```
robot-webrtc/
├── main.go                    # Entry point, route registration
├── home.go                    # Word selection + AI content
├── content.go                 # Keyword extraction processor
├── shadowreddit.go            # AI comment generation
├── storygen.go                # AI book generator
├── fantasy.go                 # Fantasy card sheets
│
├── cards/                     # Trick Evolution card game
│   ├── cards.go               # Game logic, lobby
│   ├── trick.go               # Card library UI
│   ├── cardrules.go           # Rule engine
│   ├── notecard*.go           # Notecard system
│   └── PROJECT.md
│
├── webrtc/                    # WebRTC module
│   ├── sfu.go                 # Selective Forwarding Unit
│   ├── videoconference.go     # P2P signaling
│   ├── robot.go               # Robot control UI
│   ├── video.js               # P2P client
│   ├── sfu.js                 # SFU client
│   ├── robot-control.js       # Robot interface
│   └── PROJECT.md
│
├── client/                    # Raspberry Pi robot client
│   ├── client.go              # WebRTC peer
│   ├── robot.go               # Hardware control
│   ├── motorshield.go         # Motor driver
│   ├── servo.go               # Servo control
│   └── detection.go           # Face detection
│
├── html/                      # Custom HTML DSL
│   └── html.go                # Node builder, renderers
│
├── websocket/                 # WebSocket infrastructure
│   └── websocket.go           # Hub, command registry
│
├── db/                        # Database layer
│   ├── db.go                  # GORM interface
│   └── search.go              # Bleve integration
│
├── servo/                     # gRPC servo service
│   ├── servo.proto            # Protobuf definition
│   └── servo_grpc.pb.go       # Generated code
│
├── content/                   # JSON content files
│   ├── vision.json
│   ├── rhythm.json
│   └── *.json
│
├── data/                      # Generated content
│   ├── books/                 # Story generator output
│   └── fantasy_sheets/        # Fantasy card screenshots
│
└── serverlogs/                # Daily WebRTC debug logs
```

## Running the Application

### Prerequisites
- Go 1.24+
- Node.js (for Tailwind CSS build)
- OpenAI API key (for AI features)
- Optional: Coturn server (for TURN/STUN)

### Environment Variables
```bash
OPENAI_API_KEY=sk-...           # Required for AI features
TURN_PASS=your_secret           # Required for WebRTC TURN auth
WEBRTC_DEBUG=1                  # Enable browser log streaming
ENVIRONMENT=production          # Restrict WebSocket origins
```

### Build & Run
```bash
# Install dependencies
npm install

# Run server
go run main.go

# Server starts on http://localhost:8080
```

### Access Applications
- **Home**: `http://localhost:8080/`
- **Video Conference**: `http://localhost:8080/video/`
- **Robot Control**: `http://localhost:8080/robot/`
- **Card Game**: `http://localhost:8080/game/`
- **Shadow Reddit**: `http://localhost:8080/shadowreddit/`
- **Story Generator**: `http://localhost:8080/storygen/`
- **Fantasy Cards**: `http://localhost:8080/fantasy`
- **Notecard**: `http://localhost:8080/notecard/`

### Running Robot Client (Raspberry Pi)
```bash
cd cmd/client
go build
sudo ./client  # Requires GPIO permissions
```

## Development Patterns

### Adding a New Application

1. **Create handler in new file** (e.g., `myapp.go`):
```go
func MyApp(mux *http.ServeMux, registry *CommandRegistry) {
    mux.HandleFunc("/myapp/", ServeNode(MyAppPage()))

    registry.RegisterWebsocket("myCommand", func(data string, hub *Hub, msg map[string]interface{}) {
        // Handle WebSocket command
    })
}
```

2. **Register in `main.go`**:
```go
MyApp(mux, globalRegistry)
```

3. **Create HTML using DSL**:
```go
func MyAppPage() *Node {
    return Html(
        Head(Title(Text("My App"))),
        Body(
            Div(Class("container mx-auto"),
                H1(Text("Welcome")),
            ),
        ),
    )
}
```

### WebSocket Communication Pattern

**Client** (JavaScript):
```javascript
ws.send(JSON.stringify({
    type: "myCommand",
    room: "roomID",
    data: "payload"
}));
```

**Server** (Go):
```go
registry.RegisterWebsocket("myCommand", func(data string, hub *Hub, msg map[string]interface{}) {
    room := msg["room"].(string)
    hub.Send("response", responseData, room, "")
})
```

### AI Integration Pattern

```go
client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))
resp, err := client.CreateChatCompletion(context.Background(),
    openai.ChatCompletionRequest{
        Model: openai.GPT4,
        Messages: []openai.ChatCompletionMessage{
            {Role: "system", Content: "You are..."},
            {Role: "user", Content: userPrompt},
        },
    },
)
```

## Future Enhancements

Potential areas for expansion:

**Platform**:
- Configuration-driven app loading (plugin system)
- Hot reload for development
- Shared authentication/authorization layer
- Admin dashboard for all apps

**WebRTC**:
- Recording (server-side RTP to disk)
- Simulcast for adaptive bitrate
- Screen sharing support
- Metrics/monitoring (Prometheus)

**Cards**:
- Persistent game state (DB integration)
- AI opponents using rule engine
- Tournament system
- Mobile touch controls

**AI Features**:
- Local LLM support (Ollama integration)
- Streaming responses (SSE)
- Vector search for content (pgvector)
- Fine-tuned models for specific apps

**Infrastructure**:
- Docker containerization
- Kubernetes deployment
- CI/CD pipeline
- End-to-end testing

## Contributing

This is a personal experimentation platform. Each application is relatively self-contained, making it easy to:
- Extract individual apps into standalone projects
- Experiment with new technologies
- Prototype features quickly
- Share infrastructure code

## License

ISC (see package.json)
