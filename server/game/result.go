package game

import "time"

type PlayerResult struct {
	ID         string
	Name       string
	Seat       int
	CardCount  int
	Connected  bool
	Score      int
	Winner     bool
	Loser      bool
	CalledKabo bool
	KaboFailed bool
}

type RoomMetadata struct {
	Platform       string
	ClientPlatform string
	ApplicationID  string
	InstanceID     string
	GuildID        string
	ChannelID      string
	LocationID     string
	CustomID       string
	ReferrerID     string
}

type RoundEvent struct {
	Sequence int
	At       time.Time
	Kind     string
	ActorID  string
	First    *CardRef
	Second   *CardRef
	Target   *CardRef
	Card     *Card
	Reason   string
}

type RoundResult struct {
	RoomID         string
	Platform       string
	ClientPlatform string
	ApplicationID  string
	InstanceID     string
	GuildID        string
	ChannelID      string
	LocationID     string
	CustomID       string
	ReferrerID     string
	Round          int
	StartedAt      time.Time
	EndedAt        time.Time
	EndReason      string
	CalledBy       string
	Players        []PlayerResult
	Events         []RoundEvent
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
		RoomID:         g.ID,
		Platform:       g.Platform,
		ClientPlatform: g.ClientPlatform,
		ApplicationID:  g.ApplicationID,
		InstanceID:     g.InstanceID,
		GuildID:        g.GuildID,
		ChannelID:      g.ChannelID,
		LocationID:     g.LocationID,
		CustomID:       g.CustomID,
		ReferrerID:     g.ReferrerID,
		Round:          g.RoundNumber,
		StartedAt:      g.RoundStartedAt,
		EndedAt:        g.RoundEndedAt,
		EndReason:      g.EndReason,
		CalledBy:       g.CalledBy,
		Players:        make([]PlayerResult, 0, len(g.Players)),
		Events:         cloneRoundEvents(g.events),
	}
	for seat, player := range g.Players {
		calledKabo := player.ID == g.CalledBy
		result.Players = append(result.Players, PlayerResult{
			ID:         player.ID,
			Name:       player.Name,
			Seat:       seat,
			CardCount:  countCards(player),
			Connected:  player.Connected,
			Score:      playerScore(player),
			Winner:     winners[player.ID],
			Loser:      losers[player.ID],
			CalledKabo: calledKabo,
			KaboFailed: calledKabo && !winners[player.ID],
		})
	}
	return result
}

func cloneRoundEvents(events []RoundEvent) []RoundEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]RoundEvent, len(events))
	for index, event := range events {
		cloned[index] = event
		if event.First != nil {
			first := *event.First
			cloned[index].First = &first
		}
		if event.Second != nil {
			second := *event.Second
			cloned[index].Second = &second
		}
		if event.Target != nil {
			target := *event.Target
			cloned[index].Target = &target
		}
		if event.Card != nil {
			card := *event.Card
			cloned[index].Card = &card
		}
	}
	return cloned
}
