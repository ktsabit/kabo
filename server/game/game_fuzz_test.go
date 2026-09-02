package game

import (
	"fmt"
	"sync"
	"testing"
)

// FuzzRapidOverlappingActions models the kind of burst a real room can get
// from double taps, simultaneous slaps, and stale packets. Every Apply call is
// serialized the same way as transport.Room, while the input controls which
// competing intents arrive in each burst.
func FuzzRapidOverlappingActions(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{19, 44, 91, 12, 230, 17, 88, 3, 201})
	f.Add([]byte{255, 254, 253, 252, 251, 250})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			data = []byte{0}
		}
		if len(data) > 128 {
			data = data[:128]
		}

		g := startedGame(t)
		seedRapidSlap(g, data)
		expectedCards := totalGameCards(g)
		assertPublicState(t, g, expectedCards)

		for step, value := range data {
			runFuzzStep(g, value, step)
			assertPublicState(t, g, expectedCards)
			if gPhase(g) == PhaseEnded {
				break
			}
		}
	})
}

type fuzzRequest struct {
	player  string
	message ClientMessage
}

func seedRapidSlap(g *Game, data []byte) {
	g.Lock()

	// Put two occupied matching targets in front of one shared discard. All
	// requests below use the same event cursor, so exactly one can win; later
	// correct requests are late reveals, while wrong requests may add penalties.
	g.Phase = PhaseAwaitDraw
	g.Current = 0
	g.ActorID = ""
	g.Drawn = nil
	g.Reveal = nil
	g.Gift = nil
	g.Discard = []Card{{ID: "fuzz-discard", Rank: 5, Suit: Clubs}}
	g.DiscardEventID++
	g.player("a").Cards[0] = &Card{ID: "fuzz-target-a", Rank: 5, Suit: Hearts}
	g.player("b").Cards[0] = &Card{ID: "fuzz-target-b", Rank: 5, Suit: Spades}

	count := 2 + len(data)%7
	requests := make([]fuzzRequest, 0, count)
	for index := 0; index < count; index++ {
		player := "a"
		if len(data) > 0 && data[index%len(data)]&1 == 1 {
			player = "b"
		}
		requests = append(requests, fuzzRequest{
			player: player,
			message: ClientMessage{
				Type:    "slap",
				EventID: g.DiscardEventID,
				Target:  CardRef{PlayerID: "a", Slot: 0},
			},
		})
	}
	g.Unlock()
	applyBurst(g, requests)
}

func runFuzzStep(g *Game, value byte, step int) {
	g.Lock()
	phase := g.Phase
	current := g.currentPlayerID()
	refs := occupiedRefs(g)
	eventID := g.DiscardEventID

	if current == "" || phase == PhaseEnded || phase == PhaseLobby {
		g.Unlock()
		return
	}

	requests := make([]fuzzRequest, 0, 3)
	add := func(player string, message ClientMessage) {
		requests = append(requests, fuzzRequest{player: player, message: message})
	}

	switch phase {
	case PhaseAwaitDraw:
		if len(g.Deck) > 0 {
			g.Deck[len(g.Deck)-1] = fuzzDrawCard(step, value)
		}
		add(current, ClientMessage{Type: "draw"})
		add(current, ClientMessage{Type: "draw"})
	case PhaseAwaitChoice:
		slot := firstSlotFor(g, current)
		if value&1 == 0 {
			add(current, ClientMessage{Type: "discard_drawn"})
			add(current, ClientMessage{Type: "discard_drawn"})
			if slot >= 0 {
				add(current, ClientMessage{Type: "replace", Slot: slot})
			}
		} else if slot >= 0 {
			add(current, ClientMessage{Type: "replace", Slot: slot})
			add(current, ClientMessage{Type: "replace", Slot: slot})
			add(current, ClientMessage{Type: "discard_drawn"})
		}
	case PhaseAwaitSelfPeek:
		if slot := firstSlotFor(g, current); slot >= 0 {
			add(current, ClientMessage{Type: "peek", Target: CardRef{PlayerID: current, Slot: slot}})
			add(current, ClientMessage{Type: "peek", Target: CardRef{PlayerID: current, Slot: slot}})
		}
	case PhaseAwaitOpponentPeek, PhaseAwaitKingPeek:
		if target := firstOpponentRef(g, current); target != nil {
			add(current, ClientMessage{Type: "peek", Target: *target})
			add(current, ClientMessage{Type: "peek", Target: *target})
		}
	case PhaseRevealSelf, PhaseRevealOpponent, PhaseRevealKing:
		add(current, ClientMessage{Type: "acknowledge_reveal"})
		add(current, ClientMessage{Type: "acknowledge_reveal"})
	case PhaseAwaitSwap:
		if len(refs) >= 2 {
			add(current, ClientMessage{Type: "swap", First: refs[0], Second: refs[1]})
			add(current, ClientMessage{Type: "swap", First: refs[0], Second: refs[1]})
		}
	case PhaseAwaitGift:
		if g.Gift != nil {
			sourceSlot := firstSlotFor(g, g.Gift.SlapperID)
			if sourceSlot >= 0 {
				add(g.Gift.SlapperID, ClientMessage{Type: "gift", SourceSlot: sourceSlot})
				add(g.Gift.SlapperID, ClientMessage{Type: "gift", SourceSlot: sourceSlot})
			}
		}
	case PhaseInitialPeek:
		add(current, ClientMessage{Type: "acknowledge_initial"})
	}

	// Mix a stale slap into some bursts. It must never mutate the discard
	// cursor as if it were the current event, regardless of what wins the race.
	if value%3 == 0 && eventID > 0 && len(g.Discard) > 0 {
		add("a", ClientMessage{
			Type:    "slap",
			EventID: eventID - 1,
			Target:  CardRef{PlayerID: "a", Slot: 0},
		})
	}

	// Unlock before launching the burst; each worker takes the same game lock
	// that the WebSocket transport takes around Apply.
	g.Unlock()
	applyBurst(g, requests)
}

func applyBurst(g *Game, requests []fuzzRequest) {
	var wait sync.WaitGroup
	wait.Add(len(requests))
	for _, request := range requests {
		request := request
		go func() {
			defer wait.Done()
			g.Lock()
			_ = g.Apply(request.player, request.message)
			g.Unlock()
		}()
	}
	wait.Wait()
}

func fuzzDrawCard(step int, value byte) Card {
	ranks := [...]int{2, 7, 9, 11, 13}
	return Card{ID: fmt.Sprintf("fuzz-draw-%d", step), Rank: ranks[(step+int(value))%len(ranks)], Suit: Clubs}
}

func occupiedRefs(g *Game) []CardRef {
	refs := make([]CardRef, 0, len(g.Players)*4)
	for _, player := range g.Players {
		for slot, card := range player.Cards {
			if card != nil {
				refs = append(refs, CardRef{PlayerID: player.ID, Slot: slot})
			}
		}
	}
	return refs
}

func firstSlotFor(g *Game, playerID string) int {
	player := g.player(playerID)
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

func firstOpponentRef(g *Game, playerID string) *CardRef {
	for _, player := range g.Players {
		if player.ID == playerID {
			continue
		}
		if slot := firstSlotFor(g, player.ID); slot >= 0 {
			return &CardRef{PlayerID: player.ID, Slot: slot}
		}
	}
	return nil
}

func gPhase(g *Game) Phase {
	g.Lock()
	defer g.Unlock()
	return g.Phase
}

func totalGameCards(g *Game) int {
	g.Lock()
	defer g.Unlock()
	return countGameCards(g)
}

func countGameCards(g *Game) int {
	total := len(g.Deck) + len(g.Discard)
	if g.Drawn != nil {
		total++
	}
	for _, player := range g.Players {
		for _, card := range player.Cards {
			if card != nil {
				total++
			}
		}
	}
	return total
}

func assertPublicState(t *testing.T, g *Game, expectedCards int) {
	t.Helper()
	g.Lock()
	defer g.Unlock()
	if total := countGameCards(g); total != expectedCards {
		t.Fatalf("card conservation failed during overlapping actions: got=%d want=%d", total, expectedCards)
	}

	if g.Action != nil && (g.Action.ID <= 0 || g.Action.ID != g.ActionEventID) {
		t.Fatalf("action cursor mismatch: action=%+v cursor=%d", g.Action, g.ActionEventID)
	}
	if len(g.Discard) > 0 && g.DiscardEventID <= 0 {
		t.Fatalf("discard exists without a valid event cursor: %+v id=%d", g.Discard, g.DiscardEventID)
	}
	if g.Drawn != nil && g.Phase != PhaseAwaitChoice {
		t.Fatalf("drawn card leaked into phase %s", g.Phase)
	}
	if g.Gift != nil && g.Phase != PhaseAwaitGift {
		t.Fatalf("gift leaked into phase %s", g.Phase)
	}
	if g.Phase != PhaseLobby && len(g.Players) > 0 && (g.Current < 0 || g.Current >= len(g.Players)) {
		t.Fatalf("current player index out of range: %d/%d", g.Current, len(g.Players))
	}

	for _, viewerID := range []string{"a", "b", "spectator"} {
		view := g.View(viewerID)
		if (len(g.Discard) > 0) != (view.DiscardTop != nil) {
			t.Fatalf("discard visibility mismatch for %s: pile=%d view=%+v", viewerID, len(g.Discard), view.DiscardTop)
		}
		if g.Action != nil && view.Action == nil {
			t.Fatalf("action disappeared from %s view: %+v", viewerID, g.Action)
		}
		if view.DrawPileCount != len(g.Deck) || view.HasDrawnCard != (g.Drawn != nil) {
			t.Fatalf("public draw state mismatch for %s: deck=%d/%d drawn=%t/%t", viewerID, view.DrawPileCount, len(g.Deck), view.HasDrawnCard, g.Drawn != nil)
		}
		if len(view.Players) != len(g.Players) {
			t.Fatalf("player roster disappeared from %s view: got=%d want=%d", viewerID, len(view.Players), len(g.Players))
		}
	}
}
