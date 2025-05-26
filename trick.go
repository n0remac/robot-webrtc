// cards.go – Trick Evolution card + sheet scaffolding
// ---------------------------------------------------------
// Focused on authoring / previewing physical cards.
//
// NOTE: A small chunk of print‑specific CSS must be loaded once in your
// global stylesheet (or in a <style> tag that is present for print):
//
//   @media print {
//     @page { size: 11in 8.5in; margin: 0; }
//     .print-sheet { width: 11in; height: 8.5in; page-break-after: always; }
//   }
//
// The GoDom components below reference the class .print-sheet so that each
// rendered sheet occupies exactly one landscape Letter page. If you are on
// A4, change the @page size to 297mm 210mm and keep everything else.

package main

import (
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Domain model
// -----------------------------------------------------------------------------
var baseRankOrder = map[Rank]int{
	Two: 2, Three: 3, Four: 4, Five: 5, Six: 6,
	Seven: 7, Eight: 8, Nine: 9, Ten: 10,
	Jack: 11, Queen: 12, King: 13, Ace: 14,
}

type Suit string

type Rank string

type CardCategory string

const (
	Hearts   Suit = "♥"
	Diamonds Suit = "♦"
	Clubs    Suit = "♣"
	Spades   Suit = "♠"

	Ace   Rank = "A"
	Two   Rank = "2"
	Three Rank = "3"
	Four  Rank = "4"
	Five  Rank = "5"
	Six   Rank = "6"
	Seven Rank = "7"
	Eight Rank = "8"
	Nine  Rank = "9"
	Ten   Rank = "10"
	Jack  Rank = "J"
	Queen Rank = "Q"
	King  Rank = "K"

	CategoryStandard CardCategory = "standard" // 52‑card deck
	CategoryRule     CardCategory = "rule"     // single‑round or trick rule
	CategoryPhase    CardCategory = "phase"    // multi‑round / meta rule
)

type Card struct {
	ID    string       `json:"id"`
	Suit  Suit         `json:"suit,omitempty"`
	Rank  Rank         `json:"rank,omitempty"`
	Title string       `json:"title,omitempty"` // for rule / phase cards
	Text  string       `json:"text,omitempty"`  // description / blurb
	Cat   CardCategory `json:"category"`
}

// -----------------------------------------------------------------------------
// In‑memory store (thread‑safe)
// -----------------------------------------------------------------------------

var store = struct {
	sync.RWMutex
	m map[string]Card
}{m: make(map[string]Card)}

func init() {
	seedRuleAndPhase() // custom rules provided by user
}

func seedBaseDeck() {
	suits := []Suit{Hearts, Diamonds, Clubs, Spades}
	ranks := []Rank{Ace, Two, Three, Four, Five, Six, Seven, Eight, Nine, Ten, Jack, Queen, King}
	for _, s := range suits {
		for _, r := range ranks {
			c := Card{ID: uuid.NewString(), Suit: s, Rank: r, Cat: CategoryStandard}
			store.m[c.ID] = c
		}
	}
}

// seedRuleAndPhase adds all agreed‑upon rule & phase cards.
func seedRuleAndPhase() {
	// Round / rule cards (apply within a single hand or trick cycle)
	ruleDefs := []struct{ Title, Text string }{
		// Play behaviour
		{"Follow Suit", "You must play the lead suit if able."},
		{"No Follow Required", "Play any card even if you hold the lead suit."},
		{"Must Underplay", "If able, you must play a lower card than the lead."},
		{"Must Beat Lead", "If able, you must play a higher card than the lead."},
		{"Reverse Play Order", "Play proceeds counter-clockwise this round."},

		// Win condition
		{"High Card Wins", "Highest card of the lead suit wins the trick."},
		{"Low Card Wins", "Lowest card of the lead suit wins the trick."},
		{"Even Card Wins", "Highest even‑ranked card wins the trick."},
		{"Odd Card Wins", "Highest odd‑ranked card wins the trick."},
		{"Hidden Tricks", "Tricks are kept face-down and revealed at round end."},
		{"Last Trick Wins", "Only the final trick each round counts for points."},

		// Trump rules
		{"No Trump", "No trump suit this round."},
		{"Fixed Trump", "Hearts are trump this round."},
		{"Random Trump", "Randomly select a trump suit at start of round."},
		{"Lead Chooses Trump", "Leader of first trick selects trump suit."},
		{"Per Turn Trump", "Leader declares trump suit each trick."},
		{"Jack is Trump", "All Jacks are trump cards this round."},
		{"Suitless Play", "Ignore suits. Ranks determine trick winners."},

		// Special interactions
		{"King Kills Ace", "Kings beat Aces in all suits this round."},

		// Scoring
		{"Score by Tricks", "Each trick captured = 1 point."},
		{"Red Suits Score", "Only tricks containing ♥ or ♦ score."},
		{"Avoid Queen of Spades", "Capturing Q♠ is –5 points."},
		{"Bonus for Jacks", "+2 points for each Jack captured."},
		{"Target Trick Count", "Score only if your trick total matches your bid."},
		{"Momentum Bonus", "+1 point if you win 3+ tricks in a row."},
		{"Chaos Points", "+1 point if you win exactly 0 or 5 tricks."},

		// Turn order
		{"Random Lead", "Random player leads each round."},
		{"Winner Leads", "Winner of previous trick leads next."},
		{"Fixed Rotation", "Lead passes clockwise each trick."},

		// Hand‑size modifiers
		{"+1 Hand Size", "Draw one extra card at start of round."},
		{"+2 Hand Size", "Draw two extra cards at start of round."},
		{"-1 Rule Cards", "Discard one active rule card from play."},
		{"-2 Rule Cards", "Discard two active rule cards from play."},
	}

	phaseDefs := []struct{ Title, Text string }{
		// Initial Phase Cards
		{"Carry One Card", "Keep one card from your previous hand to include in your next hand."},
		{"Reveal One Opponent Card", "Peek at one random card from an opponent’s hand at round start."},
		{"Partner Across", "Partner with the player seated directly across from you."},
		{"Bid for Lead", "Bid the number of tricks you will win. Highest bidder leads. Lose bid if you fail."},
		{"Declare Goals", "Announce the number of tricks you aim to win. Lose bid if you fail."},
		{"Two Cards Forward", "Pass two cards to the player on your left before play begins."},
		{"Swap One", "Trade one card with the player on your right before the round starts."},

		// Strategic Phase Cards
		{"Reward Exact", "Gain +1 point if your trick total matches your bid exactly."},
		{"Penalty for Overbid", "Lose 1 point for each trick over your bid."},
		{"Underscore Bonus", "Gain +1 point if you win fewer tricks than your bid (not zero)."},
		{"Carry Points", "If you hit your bid, save unused bid points for later rounds."},
		{"Solo Challenge", "Declare solo play. Score double if you meet your bid alone."},
		{"Team Bid", "Partner bids are combined. Both players must hit the total to score."},

		// Endgoal Phase Cards
		{"Fixed Rounds", "Game ends after an agreed number of rounds."},
		{"Sudden Death", "Game ends after this round. Highest score wins."},
		{"Exact Score Win", "Reach exactly 15 (or 20) points to win. Overshooting resets to 10."},
		{"Exact or Higher", "Reach 15 (or 20) points or more to win."},
		{"Lowest Score Wins", "Player with the fewest points at game end wins."},
		{"Declaring Victory", "You may declare victory before a round. Player at end of round with most tricks wins."},
		{"Shoot the Moon", "Declare that you will win all the tricks. If you do, you win. If not, the highest-scoring opponent wins."},
		{"Elimination Rounds", "Last-place player is eliminated every 3 rounds."},
		{"Sudden Surge", "If any player scores 5 points in a round, game ends immediately."},
	}

	for _, d := range ruleDefs {
		c := Card{ID: uuid.NewString(), Title: d.Title, Text: d.Text, Cat: CategoryRule}
		store.m[c.ID] = c
	}
	for _, d := range phaseDefs {
		c := Card{ID: uuid.NewString(), Title: d.Title, Text: d.Text, Cat: CategoryPhase}
		store.m[c.ID] = c
	}
}

// -----------------------------------------------------------------------------
// Public bootstrap – call Trick(mux) from main.go
// -----------------------------------------------------------------------------

func Trick(mux *http.ServeMux) {
	mux.HandleFunc("/cards", listCards)          // GET index + POST create rule
	mux.HandleFunc("/cards/sheet", sheetBuilder) // GET printable sheets
	mux.HandleFunc("/cards/", viewCard)          // GET /cards/{id}
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

func listCards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		title := strings.TrimSpace(r.FormValue("title"))
		text := strings.TrimSpace(r.FormValue("text"))
		cat := r.FormValue("cat") // "rule" or "phase"
		if title == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}
		c := Card{ID: uuid.NewString(), Title: title, Text: text, Cat: CardCategory(cat)}
		if c.Cat != CategoryRule && c.Cat != CategoryPhase {
			c.Cat = CategoryRule
		}
		store.Lock()
		store.m[c.ID] = c
		store.Unlock()
		w.Header().Set("HX-Redirect", "/cards")
		w.WriteHeader(http.StatusNoContent)
	default:
		store.RLock()
		cards := make([]Card, 0, len(store.m))
		for _, c := range store.m {
			cards = append(cards, c)
		}
		store.RUnlock()
		w.Write([]byte(CardsIndexPage(cards).Render()))
	}
}

func viewCard(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	store.RLock()
	c, ok := store.m[id]
	store.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write([]byte(CardPage(c).Render()))
}

func sheetBuilder(w http.ResponseWriter, r *http.Request) {
	// For now we only support GET to render all cards on sheets.
	store.RLock()
	cards := make([]Card, 0, len(store.m))
	for _, c := range store.m {
		cards = append(cards, c)
	}
	store.RUnlock()
	w.Write([]byte(cardSheetsPage(cards).Render()))
}

// -----------------------------------------------------------------------------
// Presentation helpers (GoDom DSL)
// -----------------------------------------------------------------------------

func CardsIndexPage(cards []Card) *Node {
	return DefaultLayout(
		Attr("hx-boost", "true"),
		Div(Class("container mx-auto p-4 space-y-6"),
			H1(Class("text-3xl font-bold"), T("Trick Evolution – Card Library")),
			CardCreateForm(),
			Div(Id("cards-list"), Class("grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-6 gap-4"),
				Ch(func() []*Node {
					out := []*Node{}
					for _, c := range cards {
						out = append(out, CardThumb(c))
					}
					return out
				}()),
			),
		),
	)
}

func CardCreateForm() *Node {
	return Form(Class("flex flex-col sm:flex-row gap-2"), Attr("hx-post", "/cards"), Attr("hx-swap", "none"),
		Select(Name("cat"), Class("select select-bordered"),
			Option(Value("rule"), T("Rule")),
			Option(Value("phase"), T("Phase")),
		),
		Input(Type("text"), Name("title"), Placeholder("Card title"), Class("input input-bordered flex-1")),
		Input(Type("text"), Name("text"), Placeholder("Optional description"), Class("input input-bordered flex-1")),
		Button(Type("submit"), Class("btn btn-primary"), T("Add")),
	)
}

func CardThumb(c Card) *Node { return A(Href("/cards/"+c.ID), cardFace(c, true)) }

func CardPage(c Card) *Node {
	return DefaultLayout(
		Div(Class("flex flex-col items-center p-8"),
			cardFace(c, false),
			A(Href("/cards"), Class("link mt-4"), T("← Back to list")),
		),
	)
}

func cardFace(c Card, thumb bool) *Node {
	if c.Cat == CategoryRule {
		return ruleCardFace(c, thumb)
	} else if c.Cat == CategoryPhase {
		return phaseCardFace(c, thumb)
	}
	return standardCardFace(c, thumb)
}

func standardCardFace(c Card, thumb bool) *Node {
	size := "w-40"
	if thumb {
		size = "w-28"
	}
	title := string(c.Rank) + " " + string(c.Suit)
	return Div(Class(size+" aspect-[2.5/3.5] border-2 border-solid rounded-lg shadow-lg bg-white text-black flex flex-col justify-between p-2"),
		Div(Class("text-sm"), T(title)),
	)
}

func ruleCardFace(c Card, thumb bool) *Node {
	size := "w-40"
	titleSize := "text-lg"
	descSize := "text-sm"
	pad := "p-4"
	if thumb {
		size = "w-28"
		titleSize = "text-sm"
		descSize = "text-xs"
		pad = "p-2"
	}
	return Div(Class(size+" aspect-[2.5/3.5] border-2 border-solid rounded-lg shadow-lg bg-white text-black flex flex-col justify-center "+pad+" text-center space-y-2"),
		Div(Class(titleSize+" font-bold"), T(c.Title)),
		Div(Class(descSize+" whitespace-pre-wrap break-words"), T(c.Text)),
	)
}

func phaseCardFace(c Card, thumb bool) *Node {
	size := "w-40"
	titleSize := "text-lg"
	descSize := "text-sm"
	pad := "p-4"
	if thumb {
		size = "w-28"
		titleSize = "text-sm"
		descSize = "text-xs"
		pad = "p-2"
	}
	return Div(Class(size+" aspect-[2.5/3.5] border-2 border-dashed rounded-lg shadow-lg bg-yellow-50 text-black flex flex-col justify-center "+pad+" text-center space-y-2"),
		Div(Class(titleSize+" font-bold"), T(c.Title)),
		Div(Class(descSize+" whitespace-pre-wrap break-words"), T(c.Text)),
	)
}

// cardSheet returns a single 4×2 printable grid (eight cards).
func cardSheet(cards []Card) *Node {
	for len(cards) < 8 {
		cards = append(cards, Card{})
	}
	panels := []*Node{}
	for _, c := range cards {
		panel := Div(Class("flex justify-center items-center w-full h-full"))
		if c.ID != "" {
			panel.Children = append(panel.Children, cardFace(c, false))
		}
		panels = append(panels, panel)
	}

	return Div(Class("print-sheet grid grid-cols-4 grid-rows-2 gap-0 border border-gray-300 mb-4"),
		Ch(panels),
	)
}

// cardSheetsPage chunks an arbitrary slice of cards into groups of eight and
// renders a stack of printable sheets (each 4×2 grid).
func cardSheetsPage(all []Card) *Node {
	sheets := []*Node{}
	for i := 0; i < len(all); i += 8 {
		end := i + 8
		if end > len(all) {
			end = len(all)
		}
		sheets = append(sheets, cardSheet(all[i:end]))
	}
	return DefaultLayout(
		Div(Class("flex flex-col items-center"),
			H1(Class("text-2xl font-bold my-4"), T("Print Sheets")),
			Ch(sheets),
			A(Href("/cards"), Class("btn btn-secondary mt-4 no-print"), T("Back")),
		),
	)
}
