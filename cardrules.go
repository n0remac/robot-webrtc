package main

import (
	"fmt"
	"sync"
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
}

func (g *Game) AllTricksPlayed() bool {
	// TODO: implement
	return false
}

func (g *Game) IsGameOver() bool {
	// TODO: implement
	return false
}

func DefaultRankComparer(a, b Card) int {
	return baseRankOrder[a.Rank] - baseRankOrder[b.Rank]
}
