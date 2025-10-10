# Trick Evolution - Card Game System

## Purpose

Trick Evolution is a multiplayer web-based trick-taking card game with a unique twist: **customizable rules**. The game uses a modular rule engine that allows players to modify game mechanics through special rule and phase cards, creating endless variations of classic trick-taking gameplay.

The system also includes a physical card authoring tool that allows users to design, preview, and print custom rule cards on standard Letter-sized sheets.

## Architecture

### Backend (Go)

The application follows a modular handler-based architecture:

```
cards/
├── cards.go          # Core game logic, multiplayer lobby, deck management
├── trick.go          # Card library UI, printable sheet generation
├── cardrules.go      # Rule engine with hooks, phases, and effects
├── notecard.go       # Voting/notecard system (separate feature)
├── notecardutils.go  # Notecard utilities
├── notecardvote.go   # Voting logic
└── cards.js          # Client-side drag-and-drop interactions
```

**Flow:**
1. Players join a lobby via `/game/` endpoint
2. WebSocket connection established for real-time communication
3. Game state synchronized across all players via WebSocket broadcasts
4. Rule engine validates and processes each action
5. UI updates sent via targeted WebSocket messages

### Frontend

- **htmx** for dynamic HTML updates without full page reloads
- **WebSockets** for real-time multiplayer communication
- **Tailwind CSS + DaisyUI** for styling
- **Drag-and-drop API** for card interactions
- Server-rendered HTML using custom Go DSL

## Technical Stack

### Core Technologies
- **Go 1.24** - Backend server
- **WebSockets** (gorilla/websocket) - Real-time communication
- **htmx** - Dynamic HTML updates
- **Tailwind CSS + DaisyUI** - UI framework
- **UUID** (google/uuid) - Unique identifiers

### Dependencies
- `github.com/gorilla/websocket` - WebSocket implementation
- `github.com/google/uuid` - ID generation
- `github.com/sashabaranov/go-openai` - AI integration (notecard feature)
- Custom HTML DSL from `github.com/n0remac/robot-webrtc/html`
- Custom WebSocket utilities from `github.com/n0remac/robot-webrtc/websocket`

## Major Components

### 1. Card System (`cards.go`, `trick.go`)

**Card Types:**
- **Standard Cards** - Traditional 52-card deck (4 suits × 13 ranks)
- **Rule Cards** - Modify gameplay mechanics (e.g., "High Card Wins", "Must Follow Suit")
- **Phase Cards** - Multi-round meta-rules (e.g., "Bid for Lead", "Partner Across")

**Features:**
- Thread-safe in-memory card store
- Card library UI at `/cards`
- Individual card view at `/cards/{id}`
- Printable sheet generator at `/cards/sheet` (4×2 grid, 8 cards per page)
- Custom card creation via web form

### 2. Rule Engine (`cardrules.go`)

A powerful hook-based system that allows dynamic modification of game rules:

**Game Phases:**
```
start_game → start_round → trick_start → trick_play → trick_end → round_end
                ↑______________________________________________|
                                (if game continues)
```

**Rule Hooks:**
- `onStartGame` - Initial setup
- `onStartRound` - Round initialization
- `onPlay` - Card play validation
- `onEndTrick` - Trick resolution
- `onScore` - Scoring calculations
- `onPhaseEnter` - Phase transition handling

**Rule Validation & Effects:**
- Rules can validate actions (e.g., enforce "must follow suit")
- Rules can produce effects (e.g., award tricks, modify scores)
- Rules are composable - multiple rules can be active simultaneously

### 3. Multiplayer System (`cards.go`)

**Lobby System:**
- Players join via `/game/join` with a room code
- Up to 4 players per room
- Real-time lobby updates via polling (htmx every 2s)
- Game starts when host clicks "Start Game"

**Game Flow:**
1. Cards dealt (5 per player)
2. Players drag cards to table area
3. WebSocket message `playCardToTrick` sent
4. Server validates move via rule engine
5. Card added to trick area
6. When all players have played, trick is resolved
7. Winner determined by active rules
8. Trick cleared, next trick begins
9. When hands are empty, new round starts

**WebSocket Commands:**
- `startCardGame` - Initialize game and deal cards
- `playCardToTrick` - Play a card to the current trick
- `discardCard` - Discard a card from hand

### 4. Notecard System (`notecard.go`, `notecardutils.go`, `notecardvote.go`)

A separate collaborative notecard system with:
- Short and long text entries
- Image generation via OpenAI
- Upvote/downvote system
- Room-based organization
- Persistent storage in JSON files

## Unique Features

### 1. **Modular Rule Engine**
Unlike traditional card games with fixed rules, Trick Evolution allows players to combine different rule cards to create custom game variants. The rule engine architecture makes it trivial to add new rules without modifying core game logic.

### 2. **Physical Card Design Tool**
The `/cards/sheet` endpoint generates print-ready layouts for creating physical cards. Players can design custom rule cards and print them on standard paper for tabletop play.

### 3. **Real-time Multiplayer Synchronization**
State updates are targeted per-player, allowing each player to see only their own hand while sharing a common view of the table area. The server acts as the authoritative source of game state.

### 4. **Extensible Card Categories**
Three distinct card categories (standard, rule, phase) allow for different levels of game modification:
- **Standard** - Basic playing cards
- **Rule** - Single-round modifications
- **Phase** - Multi-round strategic elements

### 5. **Drag-and-Drop Interface**
Intuitive card play using native HTML5 drag-and-drop API. Cards can be dragged from hand to table or discard pile with visual feedback (ring highlight on valid drop zones).

### 6. **Dynamic Rule Application**
Rules are evaluated at runtime through hooks, allowing for complex interactions between multiple active rules without hard-coding specific combinations.

## Example Rules

**Play Behavior:**
- "Follow Suit" - Must play lead suit if able
- "Must Beat Lead" - Must play higher card if able
- "Reverse Play Order" - Counter-clockwise play

**Win Conditions:**
- "High Card Wins" - Standard trick-taking
- "Low Card Wins" - Inverted scoring
- "Even Card Wins" - Only even ranks can win

**Scoring:**
- "Score by Tricks" - Each trick = 1 point
- "Avoid Queen of Spades" - Q♠ = -5 points
- "Momentum Bonus" - +1 for 3+ tricks in a row

**Phase Cards:**
- "Bid for Lead" - Auction system for first player
- "Partner Across" - Team-based play
- "Shoot the Moon" - All-or-nothing victory condition

## Running the Application

The cards system is integrated into the larger robot-webrtc application. To access:

1. Start the main server (from repo root):
   ```bash
   go run main.go
   ```

2. Access game interfaces:
   - Card library: `http://localhost:8080/cards`
   - Game lobby: `http://localhost:8080/game/`
   - Print sheets: `http://localhost:8080/cards/sheet`
   - Notecard system: `http://localhost:8080/notecard/`

## Future Enhancements

Potential areas for expansion:
- Persistent game state (database integration)
- Spectator mode
- Tournament/ladder system
- AI opponents using rule engine
- Custom card artwork upload
- Mobile-optimized touch controls
- Game replay/history
- Rule card marketplace/sharing
