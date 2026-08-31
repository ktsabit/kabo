package game

import (
	"math/rand"
	"testing"
)

func TestCardScore(t *testing.T) {
	cases := []struct {
		card Card
		want int
	}{
		{Card{Rank: 0, Suit: Joker}, 0},
		{Card{Rank: 1, Suit: Spades}, 1},
		{Card{Rank: 12, Suit: Clubs}, 12},
		{Card{Rank: 13, Suit: Clubs}, 13},
		{Card{Rank: 13, Suit: Hearts}, -1},
		{Card{Rank: 13, Suit: Diamonds}, -1},
	}
	for _, tc := range cases {
		if got := CardScore(tc.card); got != tc.want {
			t.Fatalf("CardScore(%+v) = %d, want %d", tc.card, got, tc.want)
		}
	}
}

func TestInitialCardsAreOnlyRevealedToTheirOwner(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(7)))
	_, _ = g.AddOrReconnect("a", "Ada")
	_, _ = g.AddOrReconnect("b", "Ben")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}

	viewA := g.View("a")
	if viewA.Reveal == nil || len(viewA.Reveal.Cards) != 2 {
		t.Fatalf("owner should see two initial cards: %+v", viewA.Reveal)
	}
	if viewA.Reveal.Cards[0].Target.PlayerID != "a" || viewA.Reveal.Cards[0].Target.Slot != 2 {
		t.Fatalf("unexpected initial reveal: %+v", viewA.Reveal)
	}
	for _, player := range viewA.Players {
		for _, slot := range player.Cards {
			if slot.Card != nil {
				t.Fatal("normal card grid leaked a hidden card")
			}
		}
	}
	viewB := g.View("b")
	if viewB.Reveal == nil || viewB.Reveal.Cards[0].Target.PlayerID != "b" {
		t.Fatal("the other player should receive only their own reveal")
	}
}

func TestInitialReadyStateIsVisibleToEveryone(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(8)))
	_, _ = g.AddOrReconnect("a", "Ada")
	_, _ = g.AddOrReconnect("b", "Ben")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}

	view := g.View("b")
	if view.Players[0].InitialReady == nil || *view.Players[0].InitialReady {
		t.Fatal("Ada should be shown as still looking")
	}
	if err := g.Apply("a", ClientMessage{Type: "acknowledge_initial"}); err != nil {
		t.Fatal(err)
	}
	view = g.View("b")
	if view.Players[0].InitialReady == nil || !*view.Players[0].InitialReady {
		t.Fatal("Ada's ready state should be visible to Ben")
	}
	if view.Players[1].InitialReady == nil || *view.Players[1].InitialReady {
		t.Fatal("Ben should still be shown as looking")
	}
}

func TestDiscardedSevenCreatesPrivatePeekThenAdvancesTurn(t *testing.T) {
	g := startedGame(t)
	seven := Card{ID: "test-seven", Rank: 7, Suit: Clubs}
	g.Deck = append(g.Deck, seven)

	if err := g.Apply("a", ClientMessage{Type: "draw"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("a", ClientMessage{Type: "discard_drawn"}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseAwaitSelfPeek {
		t.Fatalf("phase = %s, want self peek", g.Phase)
	}
	if err := g.Apply("a", ClientMessage{Type: "peek", Target: CardRef{PlayerID: "a", Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	if g.View("a").Reveal == nil {
		t.Fatal("power user should receive the reveal")
	}
	if g.View("b").Reveal != nil {
		t.Fatal("opponent received a private reveal")
	}
	publicPeek := g.View("b").PublicPeek
	if publicPeek == nil || publicPeek.ViewerID != "a" || publicPeek.Target.PlayerID != "a" || publicPeek.Target.Slot != 0 {
		t.Fatalf("opponent should see which slot is being peeked at without its value: %+v", publicPeek)
	}
	if err := g.Apply("a", ClientMessage{Type: "acknowledge_reveal"}); err != nil {
		t.Fatal(err)
	}
	if g.View("b").PublicPeek != nil {
		t.Fatal("public peek indicator should clear with the private reveal")
	}
	if g.Phase != PhaseAwaitDraw || g.currentPlayerID() != "b" {
		t.Fatalf("turn did not advance: phase=%s current=%s", g.Phase, g.currentPlayerID())
	}
}

func TestSlapPenaltyAndOpponentGift(t *testing.T) {
	g := startedGame(t)
	five := Card{ID: "discard-five", Rank: 5, Suit: Clubs}
	g.Discard = []Card{five}
	g.DiscardEventID = 4
	g.player("a").Cards[0] = &Card{ID: "not-a-five", Rank: 6, Suit: Spades}
	before := countCards(g.player("b"))
	wrong := g.Apply("b", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}})
	if wrong == nil || countCards(g.player("b")) != before+1 {
		t.Fatalf("wrong slap should add one card; err=%v before=%d after=%d", wrong, before, countCards(g.player("b")))
	}
	if coded, ok := wrong.(interface{ ErrorCode() string }); !ok || coded.ErrorCode() != "wrong_slap" {
		t.Fatalf("wrong slap should return its private error code: %T %v", wrong, wrong)
	}

	target := g.player("a")
	target.Cards[0] = &Card{ID: "matching-five", Rank: 5, Suit: Hearts}
	if err := g.Apply("b", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseAwaitGift || g.Gift == nil || target.Cards[0] != nil {
		t.Fatalf("opponent slap should await a gift: phase=%s gift=%+v", g.Phase, g.Gift)
	}
	if action := g.View("a").Action; action == nil || action.Kind != "slap" || action.Target == nil || *action.Target != (CardRef{PlayerID: "a", Slot: 0}) {
		t.Fatalf("correct slap should be broadcast as a card movement: %+v", action)
	}
	giftCard := g.player("b").Cards[0]
	if err := g.Apply("b", ClientMessage{Type: "gift", SourceSlot: 0}); err != nil {
		t.Fatal(err)
	}
	if target.Cards[0] != giftCard || g.player("b").Cards[0] != nil {
		t.Fatal("gift did not move the slapper card into the opponent slot")
	}
}

func TestSwapAndReplaceActionsAreBroadcast(t *testing.T) {
	g := startedGame(t)
	g.Phase = PhaseAwaitSwap
	g.ActorID = "a"
	first := CardRef{PlayerID: "a", Slot: 0}
	second := CardRef{PlayerID: "a", Slot: 1}
	if err := g.Apply("a", ClientMessage{Type: "swap", First: first, Second: second}); err != nil {
		t.Fatal(err)
	}
	action := g.View("b").Action
	if action == nil || action.Kind != "swap" || action.First == nil || *action.First != first || action.Second == nil || *action.Second != second {
		t.Fatalf("swap action was not broadcast: %+v", action)
	}

	g.Current = 0
	g.Phase = PhaseAwaitChoice
	g.Drawn = &Card{ID: "replacement", Rank: 4, Suit: Hearts}
	if err := g.Apply("a", ClientMessage{Type: "replace", Slot: 2}); err != nil {
		t.Fatal(err)
	}
	action = g.View("b").Action
	if action == nil || action.Kind != "replace" || action.Target == nil || *action.Target != (CardRef{PlayerID: "a", Slot: 2}) {
		t.Fatalf("replace action was not broadcast: %+v", action)
	}
}

func TestDrawnCardPresenceIsPublicButValueIsPrivate(t *testing.T) {
	g := startedGame(t)
	if err := g.Apply("a", ClientMessage{Type: "draw"}); err != nil {
		t.Fatal(err)
	}
	owner := g.View("a")
	opponent := g.View("b")
	if !owner.HasDrawnCard || owner.DrawnCard == nil {
		t.Fatal("active player should see the drawn card")
	}
	if !opponent.HasDrawnCard || opponent.DrawnCard != nil {
		t.Fatal("opponent should see a face-down drawn card without receiving its value")
	}
	if err := g.Apply("a", ClientMessage{Type: "discard_drawn"}); err != nil {
		t.Fatal(err)
	}
	if action := g.View("b").Action; action == nil || action.Kind != "discard" {
		t.Fatalf("drawn discard movement was not broadcast: %+v", action)
	}
}

func TestEndedRoundCanStartAgain(t *testing.T) {
	g := startedGame(t)
	g.end("called_end")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseInitialPeek || len(g.player("a").Cards) != 4 || len(g.player("b").Cards) != 4 {
		t.Fatalf("rematch was not dealt cleanly: phase=%s", g.Phase)
	}
}

func TestEndedRoundRevealsEveryRemainingCard(t *testing.T) {
	g := startedGame(t)
	g.end("called_end")
	view := g.View("a")
	for _, player := range view.Players {
		for _, slot := range player.Cards {
			if slot.Occupied && slot.Card == nil {
				t.Fatalf("ended snapshot hid %s slot %d", player.Name, slot.Slot)
			}
		}
		if player.Score == nil {
			t.Fatalf("ended snapshot omitted %s's score", player.Name)
		}
	}
}

func TestMidRoundJoinBecomesSpectatorAndCanJoinNextRound(t *testing.T) {
	g := startedGame(t)
	if _, err := g.AddOrReconnect("c", "Cleo"); err != nil {
		t.Fatal(err)
	}

	view := g.View("c")
	if view.You.ID != "c" || view.YouRole != MembershipSpectator {
		t.Fatalf("mid-round join should be a spectator: %+v", view)
	}
	if !view.NextRoundJoined || len(view.NextRoundPlayers) != 3 {
		t.Fatalf("spectator should be queued for the next round: %+v", view)
	}
	if len(view.Players) != 2 || view.Reveal != nil {
		t.Fatalf("spectator should see the active board without private reveals: %+v", view)
	}
	if err := g.Apply("c", ClientMessage{Type: "set_next_round", JoinNextRound: false}); err != nil {
		t.Fatal(err)
	}
	if view := g.View("c"); view.NextRoundJoined || len(view.NextRoundPlayers) != 2 {
		t.Fatalf("spectator should be able to leave the next-round roster: %+v", view)
	}
	if err := g.Apply("c", ClientMessage{Type: "draw"}); err == nil {
		t.Fatal("spectator should not be allowed to act in the current round")
	}
}

func TestWaitingPlayersPromoteIntoNextRound(t *testing.T) {
	g := startedGame(t)
	if _, err := g.AddOrReconnect("c", "Cleo"); err != nil {
		t.Fatal(err)
	}
	g.end("called_end")

	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseInitialPeek || len(g.Players) != 3 || g.player("c") == nil {
		t.Fatalf("queued player was not promoted: phase=%s players=%d", g.Phase, len(g.Players))
	}
	if g.waitingPlayer("c") != nil {
		t.Fatal("promoted player should leave the waiting roster")
	}
	if view := g.View("c"); view.YouRole != MembershipActive || view.Reveal == nil {
		t.Fatalf("promoted player should receive the new hand and reveal: %+v", view)
	}
}

func TestOptedOutActivePlayerBecomesSpectatorAfterRound(t *testing.T) {
	g := startedGame(t)
	if _, err := g.AddOrReconnect("c", "Cleo"); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("a", ClientMessage{Type: "set_next_round", JoinNextRound: false}); err != nil {
		t.Fatal(err)
	}
	g.end("called_end")
	if err := g.Apply("b", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if g.player("a") != nil || g.waitingPlayer("a") == nil {
		t.Fatal("an opted-out player should become a spectator for the next round")
	}
	if view := g.View("a"); view.YouRole != MembershipSpectator || view.NextRoundJoined {
		t.Fatalf("opted-out player should remain a spectator: %+v", view)
	}
}

func TestNextRoundRosterIsCappedAtEight(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(12)))
	for i := 0; i < MaxPlayers; i++ {
		if _, err := g.AddOrReconnect(string(rune('a'+i)), "Player"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.AddOrReconnect("extra", "Extra"); err != nil {
		t.Fatal(err)
	}
	view := g.View("extra")
	if view.NextRoundJoined || !view.NextRoundFull || len(view.NextRoundPlayers) != MaxPlayers {
		t.Fatalf("extra player should remain a spectator when roster is full: %+v", view)
	}
	if err := g.Apply("extra", ClientMessage{Type: "set_next_round", JoinNextRound: true}); err == nil {
		t.Fatal("full next-round roster should reject another player")
	}
}

func startedGame(t *testing.T) *Game {
	t.Helper()
	g := New("room", rand.New(rand.NewSource(11)))
	_, _ = g.AddOrReconnect("a", "Ada")
	_, _ = g.AddOrReconnect("b", "Ben")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("a", ClientMessage{Type: "acknowledge_initial"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("b", ClientMessage{Type: "acknowledge_initial"}); err != nil {
		t.Fatal(err)
	}
	return g
}
