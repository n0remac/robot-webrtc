package cards

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type RankComparer func(a, b Card) int

type Game struct {
	mu          sync.Mutex
	Deck        []Card
	Discard     []Card
	Players     []Player
	Phase       Phase
	PlayedCards []CardPlay
	RankCompare RankComparer

	CurrentTurn int
	Started     bool
}

type CardPlay struct {
	PlayerID string
	Card     Card
}

// ----- Phase Definition -----
type Phase string

const (
	PhaseStartGame  Phase = "start_game"
	PhaseStartRound Phase = "start_round"
	PhaseTrickStart Phase = "trick_start"
	PhaseTrickPlay  Phase = "trick_play"
	PhaseTrickEnd   Phase = "trick_end"
	PhaseRoundEnd   Phase = "round_end"
	PhaseGameEnd    Phase = "game_end"
)

// ----- Rule Hook Types -----
type RuleHook string

const (
	HookOnStartGame  RuleHook = "onStartGame"
	HookOnStartRound RuleHook = "onStartRound"
	HookOnPlay       RuleHook = "onPlay"
	HookOnEndTrick   RuleHook = "onEndTrick"
	HookOnScore      RuleHook = "onScore"
	HookOnPhaseEnter RuleHook = "onPhaseEnter"
)

// ----- GameAction and Effect -----
type GameAction struct {
	Type     string
	PlayerID string
	CardID   string
	Room     string
}

type Effect struct {
	Type   string
	Params map[string]interface{}
}

// ----- Rule Definition -----
type Rule struct {
	Title     string
	Text      string
	Hook      RuleHook
	Condition func(action GameAction, game *Game) bool
	Effect    func(action GameAction, game *Game) []Effect
}

// ----- Rule Engine -----
type RuleEngine struct {
	Rules []Rule
}

func RulesList() RuleEngine {
	return RuleEngine{
		Rules: []Rule{
			{
				Title: "High Card Wins",
				Text:  "Highest card wins the trick.",
				Hook:  HookOnEndTrick,
				Condition: func(action GameAction, game *Game) bool {
					return len(game.PlayedCards) == len(game.Players)
				},
				Effect: func(action GameAction, game *Game) []Effect {
					highest := game.PlayedCards[0]
					for _, p := range game.PlayedCards[1:] {
						if game.RankCompare(p.Card, highest.Card) > 0 {
							highest = p
						}
					}
					return []Effect{
						{
							Type: "award_trick",
							Params: map[string]interface{}{
								"winner": highest.PlayerID,
							},
						},
					}
				},
			},
			// Add more rules as needed
		},
	}
}

func (re *RuleEngine) ValidateAction(action GameAction, game *Game) error {
	for _, rule := range re.Rules {
		if rule.Hook == HookOnPlay && !rule.Condition(action, game) {
			return fmt.Errorf("GameAction '%s' blocked by rule: %s", action.Type, rule.Title)
		}
	}
	return nil
}

func (re *RuleEngine) ApplyEffects(action GameAction, game *Game) []Effect {
	var effects []Effect
	for _, rule := range re.Rules {
		if rule.Hook == HookOnPlay && rule.Condition(action, game) {
			effects = append(effects, rule.Effect(action, game)...)
		}
	}
	return effects
}

func (re *RuleEngine) TriggerHook(hook RuleHook, game *Game, action *GameAction) []Effect {
	var effects []Effect
	for _, rule := range re.Rules {
		if rule.Hook != hook {
			continue
		}
		if action != nil {
			if !rule.Condition(*action, game) {
				continue
			}
			effects = append(effects, rule.Effect(*action, game)...)
		} else {
			// Use a zero-value action if none is provided
			dummy := GameAction{}
			if !rule.Condition(dummy, game) {
				continue
			}
			effects = append(effects, rule.Effect(dummy, game)...)
		}
	}
	return effects
}

// ----- Phase Management -----
func (g *Game) NextPhase() {
	switch g.Phase {
	case PhaseStartGame:
		g.Phase = PhaseStartRound
	case PhaseStartRound:
		g.Phase = PhaseTrickStart
	case PhaseTrickStart:
		g.Phase = PhaseTrickPlay
	case PhaseTrickPlay:
		g.Phase = PhaseTrickEnd
	case PhaseTrickEnd:
		if g.AllTricksPlayed() {
			g.Phase = PhaseRoundEnd
		} else {
			g.Phase = PhaseTrickStart
		}
	case PhaseRoundEnd:
		if g.IsGameOver() {
			g.Phase = PhaseGameEnd
		} else {
			g.Phase = PhaseStartRound
		}
	}
	fmt.Printf("Transitioned to phase: %s\n", g.Phase)
}

func (g *Game) AllTricksPlayed() bool {
	// Check if all players have empty hands
	for _, p := range g.Players {
		if len(p.Hand) > 0 {
			return false
		}
	}
	return true
}

func (g *Game) IsGameOver() bool {
	// TODO: implement
	return false
}

func DefaultRankComparer(a, b Card) int {
	return baseRankOrder[a.Rank] - baseRankOrder[b.Rank]
}

func (g *Game) startNewRound() {
	// 1) Rebuild & shuffle deck
	deck := getStandardDeck()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	g.Deck = deck

	// 2) Clear previous trick state
	g.PlayedCards = nil

	// 3) Deal N cards to each player
	handSize := 5
	requiredCards := handSize * len(g.Players)
	if len(g.Deck) < requiredCards {
		fmt.Printf("⚠️  Not enough cards to deal new round: have %d, need %d\n", len(g.Deck), requiredCards)
		return
	}

	for i := range g.Players {
		// give them the next handSize cards
		g.Players[i].Hand = make([]Card, handSize)
		copy(g.Players[i].Hand, g.Deck[:handSize])
		// remove them from the deck
		g.Deck = g.Deck[handSize:]
	}
}
