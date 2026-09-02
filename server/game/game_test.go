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
	readyPlayers(t, g, "a", "b")
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
	readyPlayers(t, g, "a", "b")
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

func TestRoundCannotStartUntilEveryoneIsReady(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(9)))
	_, _ = g.AddOrReconnect("a", "Ada")
	_, _ = g.AddOrReconnect("b", "Ben")

	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err == nil {
		t.Fatal("round should not start before players are ready")
	}
	if view := g.View("a"); view.AllReady || view.YouReady {
		t.Fatalf("readiness should start false: %+v", view)
	}
	if err := g.Apply("a", ClientMessage{Type: "set_ready", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err == nil {
		t.Fatal("round should wait for Ben")
	}
	readyPlayers(t, g, "b")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
}

func TestInitialPeekTimeoutMakesTheRoundPlayable(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(10)))
	_, _ = g.AddOrReconnect("a", "Ada")
	_, _ = g.AddOrReconnect("b", "Ben")
	readyPlayers(t, g, "a", "b")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseAwaitDraw || g.InitialPending["a"] || g.InitialPending["b"] {
		t.Fatalf("initial timeout did not release the round: phase=%s pending=%v", g.Phase, g.InitialPending)
	}
}

func TestTurnTimeoutAutoDiscardsDrawnCard(t *testing.T) {
	g := startedGame(t)
	g.Deck = []Card{
		{ID: "keep-card", Rank: 3, Suit: Clubs},
		{ID: "timeout-card", Rank: 4, Suit: Clubs},
	}
	if err := g.Apply("a", ClientMessage{Type: "draw"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	if g.Drawn != nil || g.Phase != PhaseAwaitDraw || g.currentPlayerID() != "b" {
		t.Fatalf("draw timeout did not advance cleanly: drawn=%+v phase=%s current=%s", g.Drawn, g.Phase, g.currentPlayerID())
	}
	if len(g.Discard) == 0 || g.Discard[len(g.Discard)-1].ID != "timeout-card" {
		t.Fatalf("timed-out drawn card was not discarded: %+v", g.Discard)
	}
	if g.Action == nil || g.Action.Kind != "discard" {
		t.Fatalf("timeout discard should be visible as an action: %+v", g.Action)
	}
}

func TestDisconnectedPlayerIsImmediatelySkippedBeforeDrawing(t *testing.T) {
	g := startedGame(t)
	if g.currentPlayerID() != "a" || g.Phase != PhaseAwaitDraw {
		t.Fatalf("unexpected starting turn: phase=%s current=%s", g.Phase, g.currentPlayerID())
	}
	g.Disconnect("a")
	if g.currentPlayerID() != "b" || g.Phase != PhaseAwaitDraw {
		t.Fatalf("disconnected draw turn was not skipped: phase=%s current=%s", g.Phase, g.currentPlayerID())
	}
}

func TestDisconnectAfterDrawingWaitsForNormalResolution(t *testing.T) {
	g := startedGame(t)
	g.Deck[len(g.Deck)-1] = Card{ID: "disconnect-timeout", Rank: 4, Suit: Clubs}
	if err := g.Apply("a", ClientMessage{Type: "draw"}); err != nil {
		t.Fatal(err)
	}
	drawn := g.Drawn
	g.Disconnect("a")
	if g.currentPlayerID() != "a" || g.Phase != PhaseAwaitChoice || g.Drawn != drawn {
		t.Fatalf("in-progress draw was skipped unsafely: phase=%s current=%s drawn=%+v", g.Phase, g.currentPlayerID(), g.Drawn)
	}
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	if g.currentPlayerID() != "b" || g.Phase != PhaseAwaitDraw || g.Drawn != nil {
		t.Fatalf("normal timeout did not recover disconnected draw: phase=%s current=%s drawn=%+v", g.Phase, g.currentPlayerID(), g.Drawn)
	}
}

func TestRoundEndPreservesAnInFlightDrawnCard(t *testing.T) {
	g := startedGame(t)
	g.Deck[len(g.Deck)-1] = Card{ID: "in-flight-at-end", Rank: 6, Suit: Hearts}
	if err := g.Apply("a", ClientMessage{Type: "draw"}); err != nil {
		t.Fatal(err)
	}
	deckBeforeEnd := len(g.Deck)
	g.end("draw_pile_exhausted")
	if g.Drawn != nil || len(g.Deck) != deckBeforeEnd+1 || g.Deck[len(g.Deck)-1].ID != "in-flight-at-end" {
		t.Fatalf("round end lost the in-flight card: drawn=%+v deck=%d top=%+v", g.Drawn, len(g.Deck), g.Deck[len(g.Deck)-1])
	}
}

func TestFinishTurnSkipsDisconnectedPlayersInRotation(t *testing.T) {
	g := New("room", rand.New(rand.NewSource(17)))
	for _, player := range []struct{ id, name string }{{"a", "Ada"}, {"b", "Ben"}, {"c", "Cleo"}} {
		_, _ = g.AddOrReconnect(player.id, player.name)
	}
	readyPlayers(t, g, "a", "b", "c")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	g.Disconnect("b")
	g.finishTurn()
	if g.currentPlayerID() != "c" || g.Phase != PhaseAwaitDraw {
		t.Fatalf("rotation stopped on disconnected player: phase=%s current=%s", g.Phase, g.currentPlayerID())
	}
}

func TestPowerAndRevealTimeoutsAdvanceTheTurn(t *testing.T) {
	g := startedGame(t)
	g.Phase = PhaseAwaitSelfPeek
	g.ActorID = "a"
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseAwaitDraw || g.currentPlayerID() != "b" {
		t.Fatalf("power timeout did not advance: phase=%s current=%s", g.Phase, g.currentPlayerID())
	}

	g.Current = 0
	g.Phase = PhaseRevealSelf
	g.ActorID = "a"
	g.Reveal = &privateReveal{
		Kind:   "self",
		Viewer: "a",
		Cards:  []RevealCard{{Target: CardRef{PlayerID: "a", Slot: 0}, Card: *g.player("a").Cards[0]}},
	}
	if err := g.Timeout(); err != nil {
		t.Fatal(err)
	}
	if g.Reveal != nil || g.Phase != PhaseAwaitDraw || g.currentPlayerID() != "b" {
		t.Fatalf("reveal timeout did not advance: reveal=%+v phase=%s current=%s", g.Reveal, g.Phase, g.currentPlayerID())
	}
}

func TestRoundResultMarksFailedKaboCaller(t *testing.T) {
	g := startedGame(t)
	g.player("a").Cards = []*Card{{ID: "a1", Rank: 1, Suit: Clubs}}
	g.player("b").Cards = []*Card{{ID: "b9", Rank: 9, Suit: Clubs}}
	g.Players = append(g.Players, &Player{
		ID:               "c",
		Name:             "Cleo",
		Connected:        true,
		JoiningNextRound: true,
		Cards:            []*Card{{ID: "c13", Rank: 13, Suit: Spades}},
	})
	g.Current = 1
	g.Phase = PhaseAwaitDraw
	if err := g.Apply("b", ClientMessage{Type: "call_end"}); err != nil {
		t.Fatal(err)
	}
	result := g.Result()
	if result == nil || result.CalledBy != "b" || len(result.Players) != 3 {
		t.Fatalf("unexpected round result: %+v", result)
	}
	for _, player := range result.Players {
		switch player.ID {
		case "a":
			if !player.Winner || player.Score != 1 || player.CalledKabo || player.KaboFailed {
				t.Fatalf("Ada result = %+v", player)
			}
		case "b":
			if player.Winner || player.Score != 9 || !player.CalledKabo || !player.KaboFailed {
				t.Fatalf("Ben result = %+v", player)
			}
		case "c":
			if player.Winner || player.Loser || player.Score != 13 || player.CalledKabo || player.KaboFailed {
				t.Fatalf("Cleo result = %+v", player)
			}
		}
	}
}

func TestNextRoundStartsWithPreviousWinner(t *testing.T) {
	g := startedGame(t)
	g.player("a").Cards = []*Card{
		{Rank: 10, Suit: Clubs}, {Rank: 10, Suit: Diamonds}, {Rank: 10, Suit: Hearts}, {Rank: 10, Suit: Spades},
	}
	g.player("b").Cards = []*Card{
		{Rank: 1, Suit: Clubs}, {Rank: 1, Suit: Diamonds}, {Rank: 1, Suit: Hearts}, {Rank: 1, Suit: Spades},
	}
	g.end("called_end")
	if g.NextStarterID != "b" {
		t.Fatalf("next starter = %q, want b", g.NextStarterID)
	}
	readyPlayers(t, g, "a", "b")
	if err := g.Apply("b", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if got := g.currentPlayerID(); got != "b" {
		t.Fatalf("next round starter = %q, want b", got)
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
	wrongAction := g.View("a").Action
	if wrongAction == nil || wrongAction.Kind != "wrong_slap" || wrongAction.Card == nil || wrongAction.Card.ID != "not-a-five" {
		t.Fatalf("wrong slap should broadcast the revealed card: %+v", wrongAction)
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

func TestLateSlapIsPenalizedAndRevealsTheAcceptedCard(t *testing.T) {
	g := startedGame(t)
	five := Card{ID: "late-five", Rank: 5, Suit: Hearts}
	g.Discard = []Card{{ID: "discard-five", Rank: 5, Suit: Clubs}}
	g.DiscardEventID = 4
	g.player("a").Cards[0] = &five

	if err := g.Apply("b", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	before := countCards(g.player("a"))
	late := g.Apply("a", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}})
	if late == nil {
		t.Fatal("a stale slap should be penalized")
	}
	if coded, ok := late.(interface{ ErrorCode() string }); !ok || coded.ErrorCode() != "wrong_slap" {
		t.Fatalf("late slap should use the wrong-slap error code: %T %v", late, late)
	}
	if countCards(g.player("a")) != before+1 {
		t.Fatal("late slap should add a penalty card")
	}
	action := g.View("b").Action
	if action == nil || action.Kind != "wrong_slap" || action.ActorID != "a" || action.Card == nil || action.Card.ID != five.ID {
		t.Fatalf("late slap should broadcast the accepted card: %+v", action)
	}
}

func TestOnlyTheFirstCorrectSlapWinsForADiscard(t *testing.T) {
	g := startedGame(t)
	g.Discard = []Card{{ID: "discard-five", Rank: 5, Suit: Clubs}}
	g.DiscardEventID = 4
	g.player("a").Cards[0] = &Card{ID: "a-five", Rank: 5, Suit: Hearts}
	g.player("b").Cards[0] = &Card{ID: "b-five", Rank: 5, Suit: Spades}

	if err := g.Apply("a", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	if g.lastDiscardWasSlap != true || g.Discard[len(g.Discard)-1].ID != "a-five" {
		t.Fatalf("first correct slap did not close the race: slapped=%t top=%+v", g.lastDiscardWasSlap, g.Discard[len(g.Discard)-1])
	}

	before := countCards(g.player("b"))
	late := g.Apply("b", ClientMessage{Type: "slap", EventID: g.DiscardEventID, Target: CardRef{PlayerID: "b", Slot: 0}})
	if late == nil {
		t.Fatal("a second correct slap against the slapped top should be penalized")
	}
	if coded, ok := late.(interface{ ErrorCode() string }); !ok || coded.ErrorCode() != "wrong_slap" {
		t.Fatalf("second slap should use the wrong-slap error code: %T %v", late, late)
	}
	if countCards(g.player("b")) != before+1 {
		t.Fatalf("second slap should add one penalty card: before=%d after=%d", before, countCards(g.player("b")))
	}
	if g.player("b").Cards[0] == nil || g.Discard[len(g.Discard)-1].ID != "a-five" {
		t.Fatal("a penalized second slap should not move another card onto the discard pile")
	}
}

func TestSlapAfterAnOpponentSlapIsPenalizedBeforeGiftResolution(t *testing.T) {
	g := startedGame(t)
	g.Discard = []Card{{ID: "discard-five", Rank: 5, Suit: Clubs}}
	g.DiscardEventID = 4
	g.player("a").Cards[0] = &Card{ID: "a-five", Rank: 5, Suit: Hearts}
	g.player("a").Cards[1] = &Card{ID: "a-second-five", Rank: 5, Suit: Diamonds}

	if err := g.Apply("b", ClientMessage{Type: "slap", EventID: 4, Target: CardRef{PlayerID: "a", Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseAwaitGift || g.Gift == nil {
		t.Fatalf("opponent slap should still wait for its gift: phase=%s gift=%+v", g.Phase, g.Gift)
	}

	before := countCards(g.player("a"))
	late := g.Apply("a", ClientMessage{Type: "slap", EventID: g.DiscardEventID, Target: CardRef{PlayerID: "a", Slot: 1}})
	if late == nil {
		t.Fatal("a slap during the closed opponent-slap race should be penalized")
	}
	if coded, ok := late.(interface{ ErrorCode() string }); !ok || coded.ErrorCode() != "wrong_slap" {
		t.Fatalf("slap during gift resolution should use the wrong-slap error code: %T %v", late, late)
	}
	if countCards(g.player("a")) != before+1 {
		t.Fatalf("slap during gift resolution should add one penalty card: before=%d after=%d", before, countCards(g.player("a")))
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
	oldID := g.player("a").Cards[2].ID
	g.Drawn = &Card{ID: "replacement", Rank: 4, Suit: Hearts}
	if err := g.Apply("a", ClientMessage{Type: "replace", Slot: 2}); err != nil {
		t.Fatal(err)
	}
	action = g.View("b").Action
	if action == nil || action.Kind != "replace" || action.Target == nil || *action.Target != (CardRef{PlayerID: "a", Slot: 2}) || action.Card == nil || action.Card.ID != oldID {
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
	drawnID := owner.DrawnCard.ID
	if err := g.Apply("a", ClientMessage{Type: "discard_drawn"}); err != nil {
		t.Fatal(err)
	}
	if action := g.View("b").Action; action == nil || action.Kind != "discard" || action.Card == nil || action.Card.ID != drawnID {
		t.Fatalf("drawn discard movement was not broadcast: %+v", action)
	}
}

func TestEndedRoundCanStartAgain(t *testing.T) {
	g := startedGame(t)
	g.end("called_end")
	readyPlayers(t, g, "a", "b")
	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if g.Phase != PhaseInitialPeek || len(g.player("a").Cards) != 4 || len(g.player("b").Cards) != 4 {
		t.Fatalf("rematch was not dealt cleanly: phase=%s", g.Phase)
	}
}

func TestEventCursorsRemainMonotonicAcrossRounds(t *testing.T) {
	g := startedGame(t)
	g.DiscardEventID = 17
	g.ActionEventID = 23
	g.end("called_end")
	readyPlayers(t, g, "a", "b")

	if err := g.Apply("a", ClientMessage{Type: "start_game"}); err != nil {
		t.Fatal(err)
	}
	if g.DiscardEventID != 17 || g.ActionEventID != 23 {
		t.Fatalf("round reset reused event cursors: discard=%d action=%d", g.DiscardEventID, g.ActionEventID)
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
	readyPlayers(t, g, "a", "b", "c")

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
	readyPlayers(t, g, "b", "c")
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
	readyPlayers(t, g, "a", "b")
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

func readyPlayers(t *testing.T, g *Game, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := g.Apply(id, ClientMessage{Type: "set_ready", Ready: true}); err != nil {
			t.Fatalf("ready %s: %v", id, err)
		}
	}
}
