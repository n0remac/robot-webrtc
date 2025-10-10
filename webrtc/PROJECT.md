# WebRTC Module - Robot Control & Video Conferencing

## Purpose

The WebRTC module provides real-time video/audio streaming and robot control capabilities using WebRTC technology. It enables:
- Remote robot control with live video feed
- Multi-party video conferencing (mesh and SFU modes)
- Bidirectional data channels for keyboard/control commands
- NAT traversal via STUN/TURN servers

## Architecture

### Server-Side (Go)
The server uses the [Pion WebRTC](https://github.com/pion/webrtc) library and implements two distinct architectures:

1. **Peer-to-Peer Mesh** (`videoconference.go`): Traditional WebRTC signaling where each peer connects directly to every other peer
2. **Selective Forwarding Unit (SFU)** (`sfu.go`): Centralized media router that receives streams from publishers and forwards them to subscribers

### Client-Side (JavaScript)
Three specialized client implementations:
- `video.js`: Mesh P2P video conferencing client
- `sfu.js`: SFU-based video conferencing client
- `robot-control.js`: Dedicated robot control interface

### Signaling Layer
WebSocket-based signaling handled via the parent `websocket` package:
- Room-based peer discovery
- SDP offer/answer exchange
- ICE candidate trickle
- Peer join/leave notifications

## Technical Stack

### Backend
- **Language**: Go
- **WebRTC Library**: Pion WebRTC v4
- **WebSocket**: Gorilla WebSocket
- **Signaling**: Custom command registry pattern
- **Media Processing**: RTP packet forwarding, RTCP relay (PLI/FIR)

### Frontend
- **WebRTC API**: Browser native RTCPeerConnection
- **Signaling**: WebSocket with JSON messages
- **UI Framework**: DaisyUI + TailwindCSS
- **Logging**: Custom browser-to-server log streaming

### Infrastructure
- **TURN/STUN**: Coturn integration with HMAC-SHA1 time-limited credentials
- **ICE**: Google STUN + private TURN server (turn.noremac.dev)

## Major Components

### Server Components

#### 1. `sfu.go` - Selective Forwarding Unit (530 lines)
**Purpose**: Centralized media server that scales better than mesh topology

**Key Features**:
- Single PeerConnection per participant
- Publisher track fanout to all subscribers
- Automatic track cleanup on publisher disconnect
- Perfect negotiation pattern with coalesced renegotiation
- ICE restart on connection failure
- RTCP relay for Picture Loss Indication (PLI/FIR)

**Data Structures**:
- `sfuServer`: Global room registry
- `sfuRoom`: Per-room peer and track management
- `sfuPeer`: Per-participant connection state with sender tracking
- `pubTrack`: Publisher track metadata for fanout

**Endpoints**: `/ws/sfu?room=<roomID>&id=<peerID>`

#### 2. `videoconference.go` - Mesh Signaling (263 lines)
**Purpose**: WebRTC signaling for full-mesh peer-to-peer connections

**Key Features**:
- WebSocket command handlers for offer/answer/candidate/join/leave
- TURN credential generation (HMAC-SHA1 with TTL)
- Room-based message routing
- Mode selection UI (mesh vs SFU toggle)

**Endpoints**:
- `/video/` - Video conference HTML page
- `/robot/` - Robot control HTML page
- `/ws/hub` - Mesh signaling WebSocket
- `/turn-credentials` - Dynamic TURN auth

#### 3. `robot.go` - Robot Control UI (135 lines)
**Purpose**: Renders robot control interface with keyboard bindings

**Layout**:
- Video preview element
- Keyboard control grid (WASD movement, TFGH claw, IJKL camera, RY open/close)
- Servo angle display (polled from `/api/servo-angles`)
- Connect button to initiate WebRTC session

### Client Components

#### 1. `video.js` - P2P Mesh Client (549 lines)
**Purpose**: Full-mesh video conferencing with DataChannel support

**Key Features**:
- Perfect negotiation pattern with polite/impolite peer roles
- Dynamic peer creation on first message
- Automatic video element creation per peer
- ICE candidate queuing before remote description
- Graceful rollback on negotiation collisions
- DataChannel for keyboard events
- Camera/mic testing UI

**Connection Flow**:
1. Get TURN credentials
2. Acquire local media (getUserMedia)
3. Connect WebSocket to `/ws/hub`
4. Send `join` message to room
5. Create RTCPeerConnection for each peer
6. Exchange SDP via WebSocket
7. Trickle ICE candidates
8. Render remote video on `ontrack`

#### 2. `sfu.js` - SFU Client (346 lines)
**Purpose**: Simplified client for SFU architecture

**Key Differences from Mesh**:
- Single RTCPeerConnection to server
- Receive-only transceivers for initial negotiation
- Server-driven renegotiation when publishers join/leave
- `peer-left` message handler to remove remote streams
- Track-to-element mapping via `remoteTrackMap`
- Stream-based cleanup using `remoteByStream`

**Optimization**: Always polite (server is impolite) to simplify collision resolution

#### 3. `robot-control.js` - Robot Interface (415 lines)
**Purpose**: Dedicated controller for robot with keyboard/button input

**Key Features**:
- Connects to single robot peer (ID: "robot")
- DataChannel for sending key press/release events
- Combined MediaStream for video+audio
- Button-based touch controls (mousedown/mouseup/touchstart/touchend)
- Real-time servo angle polling (400ms interval)
- Auto-reconnect on WebSocket disconnect

**Control Mapping**:
- WASD: Robot movement
- TFGH: Claw positioning
- IJKL: Camera pan/tilt
- RY: Claw open/close

#### 4. Supporting Files
- `logger.js`: Browser console logger with server-side persistence via `/ws/logs`
- `media-controls.js`: Media control abstractions (mute, video toggle, noise suppression)
- `video.css`: Dark theme styling for video grids and controls

## Unique Features

### 1. Hybrid Mesh + SFU Architecture
**Innovation**: Single codebase supports both topologies with runtime mode selection

Users can toggle between:
- **Mesh**: Low-latency P2P (ideal for 2-4 participants)
- **SFU**: Server-routed (scales to 10+ participants)

Implementation uses query parameter `?mode=sfu` to load different JavaScript and WebSocket endpoints.

### 2. Coalesced Renegotiation (SFU)
**Problem**: SFU must renegotiate whenever tracks are added/removed, causing signaling storms

**Solution** (`sfu.go:406-478`):
- Negotiation worker goroutine per peer
- Debounced channel (25ms) to batch multiple track changes
- Wait for signaling state to stabilize before creating offer
- Atomic offer creation and local description setting
- Retry on next signal if glare occurs

**Benefit**: Reduces SDP exchanges from O(N²) to O(N) when N peers join simultaneously

### 3. Dual DataChannel Establishment
**Pattern**: Both peers create outgoing DataChannels and handle incoming ones

**Why?** (`video.js:285-310`, `robot-control.js:94-112`):
- Avoids race conditions in negotiation ordering
- Ensures channel exists regardless of which peer initiates
- Fallback if one direction fails

Identified by label `keyboard`, used to transmit JSON key events.

### 4. HMAC-SHA1 Time-Limited TURN Credentials
**Security**: Prevents credential theft/reuse (`videoconference.go:255-262`)

**Flow**:
1. Client requests `/turn-credentials?user=<username>`
2. Server generates: `username = <expiry>:<user>`, `password = HMAC-SHA1(secret, username)`
3. Coturn validates HMAC and expiry on connection
4. Credentials expire in 3600s (configurable)

**Benefit**: No persistent credential storage; automatic rotation

### 5. ICE Candidate Buffering
**Challenge**: Candidates arrive before remote description is set

**Solution** (all clients):
- `queuedCandidates` array in RTCPeerConnection
- Buffer incoming candidates when `!pc.remoteDescription`
- Flush queue immediately after `setRemoteDescription()`

**Prevents**: "Cannot add ICE candidate without remote description" errors

### 6. Server-Side Track Fanout
**SFU Optimization** (`sfu.go:335-356`):
- Dedicated goroutine per published track reads RTP packets
- Writes same packet to all subscriber `TrackLocalStaticRTP` instances
- Lock-free read using copied subscriber list
- Minimal server CPU (no transcoding)

**Contrast with Mesh**: Eliminates N×(N-1) connections for N peers

### 7. Browser Log Streaming
**Feature**: Real-time browser console forwarding to server (`logger.js`)

**Architecture**:
- Client WebSocket to `/ws/logs` (enabled via `WEBRTC_DEBUG` env var)
- Server writes to daily log file (`serverlogs/YYYY-MM-DD.webrtc.log`)
- Mirrors to server stdout
- Structured JSON logging with context

**Use Case**: Debug WebRTC issues in production without browser DevTools access

### 8. Perfect Negotiation Pattern
**Standard**: [WHATWG spec](https://w3c.github.io/webrtc-pc/#perfect-negotiation-example)

**Implementation** (all clients):
- Polite peer always rolls back on collision
- Impolite peer ignores incoming offer during collision
- Deterministic role assignment (UUID comparison or server=impolite)

**Reliability**: Eliminates glare deadlocks in bidirectional negotiation

## File Descriptions

### Go Files
| File | Lines | Purpose |
|------|-------|---------|
| `sfu.go` | 530 | Selective Forwarding Unit media server |
| `videoconference.go` | 263 | Mesh signaling and page rendering |
| `robot.go` | 135 | Robot control UI handler |

### JavaScript Files
| File | Lines | Purpose |
|------|-------|---------|
| `video.js` | 549 | P2P mesh video conference client |
| `sfu.js` | 346 | SFU video conference client |
| `robot-control.js` | 415 | Robot remote control client |
| `logger.js` | ~100 | Browser-to-server logging |
| `media-controls.js` | ~50 | Media device controls |

### CSS Files
| File | Purpose |
|------|---------|
| `video.css` | Dark theme video grid layout |

## Integration with Parent Application

The WebRTC module integrates with the main application via:

1. **Command Registry** (`websocket.CommandRegistry`): Pluggable signaling handlers
   - Registered in `main.go` via `VideoHandler(mux, globalRegistry)`
   - Commands: `join`, `offer`, `answer`, `candidate`, `leave`

2. **HTTP Multiplexer**: Standard Go HTTP routing
   - `/video/` → Video conference page
   - `/robot/` → Robot control page
   - `/ws/hub` → Mesh signaling
   - `/ws/sfu` → SFU signaling
   - `/turn-credentials` → Auth endpoint

3. **HTML Package**: Server-side HTML generation (`html.go`)
   - Type-safe HTML builder (no templates)
   - DaisyUI/Tailwind integration
   - JS/CSS inline embedding via `LoadFile()`

4. **WebSocket Hub** (`websocket.WsHub`): Global broadcast bus
   - Room-based message routing
   - Automatic client registration/cleanup
   - Supports targeted messages via `Id` field

## Development Notes

### Running SFU Mode
```bash
# Start server
TURN_PASS=your_secret go run main.go

# Open browser
http://localhost:8080/video/?mode=sfu&room=test
```

### Running Robot Control
```bash
# Navigate to robot interface
http://localhost:8080/robot/

# Ensure robot peer with ID "robot" is connected to room "robot"
```

### Debugging
```bash
# Enable browser log streaming
WEBRTC_DEBUG=1 go run main.go

# Tail logs
tail -f serverlogs/$(date +%Y-%m-%d).webrtc.log
```

### Key Environment Variables
- `TURN_PASS`: Coturn secret for HMAC credential generation
- `WEBRTC_DEBUG`: Enable `/ws/logs` endpoint (1/true/yes)
- `ENVIRONMENT`: Set to "production" to restrict WebSocket origins

## Future Enhancements

Potential improvements identified in code:
1. **Simulcast**: Multiple quality tiers for adaptive bitrate (commented in SFU)
2. **Recording**: Server-side RTP recording to disk
3. **Data Channel Reliability**: Configure ordered/unordered, retransmits
4. **Codec Negotiation**: Prefer VP9/AV1 over VP8 when available
5. **Metrics**: Prometheus integration for track counts, bandwidth, packet loss
6. **Room Limits**: Max participants per room enforcement
7. **Admin API**: REST endpoints to list rooms, kick peers, adjust bitrates
