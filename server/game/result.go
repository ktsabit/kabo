package game

import "time"

type PlayerResult struct {
	ID         string
	Name       string
	Score      int
	Winner     bool
	Loser      bool
	CalledKabo bool
	KaboFailed bool
}

type RoundResult struct {
	RoomID    string
	Round     int
	StartedAt time.Time
	EndedAt   time.Time
	EndReason string
	CalledBy  string
	Players   []PlayerResult
}

func (g *Game) Result() *RoundResult {
	if g.Phase != PhaseEnded || g.RoundNumber == 0 {
		return nil
	}

	winners := make(map[string]bool, len(g.WinnerIDs))
	for _, id := range g.WinnerIDs {
		winners[id] = true
	}
	losers := make(map[string]bool, len(g.LoserIDs))
	for _, id := range g.LoserIDs {
		losers[id] = true
	}
	result := &RoundResult{
		RoomID:    g.ID,
		Round:     g.RoundNumber,
		StartedAt: g.RoundStartedAt,
		EndedAt:   g.RoundEndedAt,
		EndReason: g.EndReason,
		CalledBy:  g.CalledBy,
		Players:   make([]PlayerResult, 0, len(g.Players)),
	}
	for _, player := range g.Players {
		calledKabo := player.ID == g.CalledBy
		result.Players = append(result.Players, PlayerResult{
			ID:         player.ID,
			Name:       player.Name,
			Score:      playerScore(player),
			Winner:     winners[player.ID],
			Loser:      losers[player.ID],
			CalledKabo: calledKabo,
			KaboFailed: calledKabo && !winners[player.ID],
		})
	}
	return result
}
