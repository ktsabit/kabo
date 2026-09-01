package game

import "fmt"

const (
	TimerNone   = "none"
	TimerTurn   = "turn"
	TimerReveal = "reveal"
)

// TimeoutKey identifies the phase/action that owns a deadline. It deliberately
// excludes ActionEventID and DiscardEventID because spectators' slaps and
// duplicate packets must not extend the active player's timer.
func (g *Game) TimeoutKey() string {
	switch g.Phase {
	case PhaseLobby, PhaseEnded:
		return ""
	case PhaseInitialPeek:
		return string(g.Phase)
	default:
		return fmt.Sprintf("%s:%s:%s", g.Phase, g.currentPlayerID(), g.ActorID)
	}
}

func (g *Game) TimeoutKind() string {
	switch g.Phase {
	case PhaseLobby, PhaseEnded:
		return TimerNone
	case PhaseRevealSelf, PhaseRevealOpponent, PhaseRevealKing:
		return TimerReveal
	default:
		return TimerTurn
	}
}

// Timeout applies the safe server-side fallback for a phase whose deadline
// elapsed. It is called under the room/game lock by the transport layer.
func (g *Game) Timeout() error {
	switch g.Phase {
	case PhaseInitialPeek:
		for id, pending := range g.InitialPending {
			if pending {
				g.InitialPending[id] = false
			}
		}
		g.Phase = PhaseAwaitDraw
		return nil
	case PhaseAwaitDraw:
		g.finishTurn()
		return nil
	case PhaseAwaitChoice:
		if g.Drawn != nil {
			return g.discardDrawn(g.currentPlayerID())
		}
		g.finishTurn()
		return nil
	case PhaseAwaitSelfPeek, PhaseAwaitOpponentPeek, PhaseAwaitKingPeek, PhaseAwaitSwap:
		g.finishTurn()
		return nil
	case PhaseRevealSelf, PhaseRevealOpponent, PhaseRevealKing:
		return g.acknowledgeReveal(g.currentPlayerID())
	case PhaseAwaitGift:
		if g.Gift == nil {
			g.finishTurn()
			return nil
		}
		if sourceSlot := firstOccupiedSlot(g.player(g.Gift.SlapperID)); sourceSlot >= 0 {
			return g.gift(g.Gift.SlapperID, sourceSlot)
		}
		g.Gift = nil
		g.finishTurn()
		return nil
	default:
		return nil
	}
}

func firstOccupiedSlot(player *Player) int {
	if player == nil {
		return -1
	}
	for slot, card := range player.Cards {
		if card != nil {
			return slot
		}
	}
	return -1
}
