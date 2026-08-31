package game

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
)

type Phase string

const (
	PhaseLobby             Phase = "lobby"
	PhaseInitialPeek       Phase = "initial_peek"
	PhaseAwaitDraw         Phase = "await_draw"
	PhaseAwaitChoice       Phase = "await_choice"
	PhaseAwaitSelfPeek     Phase = "await_self_peek"
	PhaseAwaitOpponentPeek Phase = "await_opponent_peek"
	PhaseRevealSelf        Phase = "reveal_self"
	PhaseRevealOpponent    Phase = "reveal_opponent"
	PhaseAwaitSwap         Phase = "await_swap"
	PhaseAwaitKingPeek     Phase = "await_king_peek"
	PhaseRevealKing        Phase = "reveal_king"
	PhaseAwaitGift         Phase = "await_gift"
	PhaseEnded             Phase = "ended"
)

const MaxPlayers = 8

const (
	MembershipActive    = "active"
	MembershipSpectator = "spectator"
)

type Player struct {
	ID               string
	Name             string
	Connected        bool
	JoiningNextRound bool
	Ready            bool
	Cards            []*Card
}

type WaitingPlayer struct {
	ID               string
	Name             string
	Connected        bool
	JoiningNextRound bool
	Ready            bool
}

type privateReveal struct {
	Kind   string
	Viewer string
	Cards  []RevealCard
}

type pendingGift struct {
	SlapperID string
	Target    CardRef
	Resume    Phase
}

type Game struct {
	mu sync.Mutex

	ID             string
	Players        []*Player
	Waiting        []*WaitingPlayer
	Phase          Phase
	Current        int
	Deck           []Card
	Discard        []Card
	Drawn          *Card
	DiscardEventID int
	ActionEventID  int
	InitialPending map[string]bool
	Reveal         *privateReveal
	Action         *ActionView
	Gift           *pendingGift
	ActorID        string
	EndReason      string
	WinnerIDs      []string
	NextStarterID  string
	rng            *rand.Rand
}

func New(id string, rng *rand.Rand) *Game {
	if rng == nil {
		rng = rand.New(rand.NewSource(rand.Int63()))
	}
	return &Game{ID: id, Phase: PhaseLobby, InitialPending: map[string]bool{}, rng: rng}
}

func (g *Game) Lock()   { g.mu.Lock() }
func (g *Game) Unlock() { g.mu.Unlock() }

func (g *Game) AddOrReconnect(id, name string) (*Player, error) {
	for _, p := range g.Players {
		if p.ID == id {
			p.Connected = true
			if name != "" {
				p.Name = name
			}
			return p, nil
		}
	}
	for _, p := range g.Waiting {
		if p.ID == id {
			p.Connected = true
			if name != "" {
				p.Name = name
			}
			return nil, nil
		}
	}
	if name == "" {
		name = "Player"
	}
	if g.Phase == PhaseLobby && len(g.Players) < MaxPlayers {
		p := &Player{ID: id, Name: name, Connected: true, JoiningNextRound: true}
		g.Players = append(g.Players, p)
		return p, nil
	}

	p := &WaitingPlayer{
		ID:               id,
		Name:             name,
		Connected:        true,
		JoiningNextRound: g.nextRoundCount() < MaxPlayers,
	}
	g.Waiting = append(g.Waiting, p)
	return nil, nil
}

func (g *Game) Disconnect(id string) {
	if p := g.player(id); p != nil {
		p.Connected = false
		if g.Phase == PhaseEnded {
			p.JoiningNextRound = false
		}
		return
	}
	if p := g.waitingPlayer(id); p != nil {
		p.Connected = false
		if g.Phase == PhaseEnded {
			p.JoiningNextRound = false
		}
	}
}

func (g *Game) Apply(playerID string, msg ClientMessage) error {
	active := g.player(playerID)
	waiting := g.waitingPlayer(playerID)
	if active == nil && waiting == nil {
		return errors.New("player is not in this room")
	}
	if msg.Type == "set_next_round" {
		return g.setNextRound(playerID, msg.JoinNextRound)
	}
	if msg.Type == "set_ready" {
		return g.setReady(playerID, msg.Ready)
	}
	if active == nil {
		return errors.New("spectators cannot perform game actions")
	}
	if g.Phase == PhaseEnded && msg.Type != "start_game" {
		return errors.New("the round has ended")
	}

	switch msg.Type {
	case "start_game":
		return g.start()
	case "acknowledge_initial":
		return g.acknowledgeInitial(playerID)
	case "draw":
		return g.draw(playerID)
	case "replace":
		return g.replace(playerID, msg.Slot)
	case "discard_drawn":
		return g.discardDrawn(playerID)
	case "peek":
		return g.peek(playerID, msg.Target)
	case "acknowledge_reveal":
		return g.acknowledgeReveal(playerID)
	case "swap":
		return g.swap(playerID, msg.First, msg.Second)
	case "slap":
		return g.slap(playerID, msg.EventID, msg.Target)
	case "gift":
		return g.gift(playerID, msg.SourceSlot)
	case "call_end":
		return g.callEnd(playerID)
	default:
		return fmt.Errorf("unknown action %q", msg.Type)
	}
}

func (g *Game) start() error {
	if g.Phase != PhaseLobby && g.Phase != PhaseEnded {
		return errors.New("game is already started")
	}
	if !g.allNextRoundReady() {
		return actionError{code: "not_ready", message: "everyone must be ready before the round can start"}
	}
	starterID := g.NextStarterID
	if g.Phase == PhaseEnded {
		g.Phase = PhaseLobby
		g.resetRoundState()
	}
	g.prepareNextRound()
	connected := g.Players[:0]
	for _, p := range g.Players {
		if p.Connected {
			p.Cards = nil
			connected = append(connected, p)
		}
	}
	g.Players = connected
	if len(g.Players) < 2 {
		return errors.New("at least two players are required")
	}
	g.Deck = newDeck()
	g.rng.Shuffle(len(g.Deck), func(i, j int) { g.Deck[i], g.Deck[j] = g.Deck[j], g.Deck[i] })
	for round := 0; round < 4; round++ {
		for _, p := range g.Players {
			card, ok := g.takeDeckCard()
			if !ok {
				return errors.New("not enough cards to deal")
			}
			p.Cards = append(p.Cards, card)
		}
	}
	g.InitialPending = map[string]bool{}
	for _, p := range g.Players {
		g.InitialPending[p.ID] = true
	}
	g.Current = 0
	if starterID != "" {
		for index, p := range g.Players {
			if p.ID == starterID {
				g.Current = index
				break
			}
		}
	}
	g.NextStarterID = ""
	g.Phase = PhaseInitialPeek
	return nil
}

func (g *Game) resetRoundState() {
	g.Deck = nil
	g.Discard = nil
	g.Drawn = nil
	g.DiscardEventID = 0
	g.ActionEventID = 0
	g.InitialPending = map[string]bool{}
	g.Reveal = nil
	g.Action = nil
	g.Gift = nil
	g.ActorID = ""
	g.EndReason = ""
	g.WinnerIDs = nil
}

func (g *Game) prepareNextRound() {
	selected := make([]*Player, 0, MaxPlayers)
	for _, p := range g.Players {
		if p.Connected && p.JoiningNextRound && len(selected) < MaxPlayers {
			p.Cards = nil
			selected = append(selected, p)
		}
	}

	waiting := make([]*WaitingPlayer, 0, len(g.Waiting)+len(g.Players))
	for _, p := range g.Players {
		if p.Connected && (!p.JoiningNextRound || !containsPlayer(selected, p.ID)) {
			waiting = append(waiting, &WaitingPlayer{
				ID:        p.ID,
				Name:      p.Name,
				Connected: p.Connected,
				Ready:     false,
			})
		}
	}
	for _, p := range g.Waiting {
		if p.JoiningNextRound && p.Connected && len(selected) < MaxPlayers {
			selected = append(selected, &Player{
				ID:               p.ID,
				Name:             p.Name,
				Connected:        true,
				JoiningNextRound: true,
				Ready:            p.Ready,
			})
			continue
		}
		p.JoiningNextRound = false
		p.Ready = false
		waiting = append(waiting, p)
	}
	g.Players = selected
	g.Waiting = waiting
}

func containsPlayer(players []*Player, id string) bool {
	for _, p := range players {
		if p.ID == id {
			return true
		}
	}
	return false
}

func (g *Game) setNextRound(playerID string, join bool) error {
	if g.Phase == PhaseLobby {
		return errors.New("next-round choices are not available in the lobby")
	}
	if p := g.player(playerID); p != nil {
		if p.JoiningNextRound == join {
			if !join {
				p.Ready = false
			}
			return nil
		}
		if join && g.nextRoundCount() >= MaxPlayers {
			return actionError{code: "next_round_full", message: "the next round is full"}
		}
		p.JoiningNextRound = join
		if !join {
			p.Ready = false
		}
		return nil
	}
	if p := g.waitingPlayer(playerID); p != nil {
		if p.JoiningNextRound == join {
			if !join {
				p.Ready = false
			}
			return nil
		}
		if join && g.nextRoundCount() >= MaxPlayers {
			return actionError{code: "next_round_full", message: "the next round is full"}
		}
		p.JoiningNextRound = join
		if !join {
			p.Ready = false
		}
		return nil
	}
	return errors.New("player is not in this room")
}

func (g *Game) setReady(playerID string, ready bool) error {
	if g.Phase != PhaseLobby && g.Phase != PhaseEnded {
		return errors.New("ready status can only change in the lobby")
	}
	if p := g.player(playerID); p != nil {
		if ready && !p.JoiningNextRound {
			return errors.New("join the next round before marking ready")
		}
		p.Ready = ready
		return nil
	}
	if p := g.waitingPlayer(playerID); p != nil {
		if ready && !p.JoiningNextRound {
			return errors.New("join the next round before marking ready")
		}
		p.Ready = ready
		return nil
	}
	return errors.New("player is not in this room")
}

func (g *Game) nextRoundCount() int {
	count := 0
	for _, p := range g.Players {
		if p.JoiningNextRound {
			count++
		}
	}
	for _, p := range g.Waiting {
		if p.JoiningNextRound {
			count++
		}
	}
	return count
}

func (g *Game) allNextRoundReady() bool {
	count := 0
	for _, p := range g.Players {
		if !p.JoiningNextRound {
			continue
		}
		count++
		if !p.Connected || !p.Ready {
			return false
		}
	}
	for _, p := range g.Waiting {
		if !p.JoiningNextRound {
			continue
		}
		count++
		if !p.Connected || !p.Ready {
			return false
		}
	}
	return count >= 2
}

func (g *Game) acknowledgeInitial(playerID string) error {
	if g.Phase != PhaseInitialPeek || !g.InitialPending[playerID] {
		return errors.New("there is no initial reveal to hide")
	}
	g.InitialPending[playerID] = false
	for _, pending := range g.InitialPending {
		if pending {
			return nil
		}
	}
	g.Phase = PhaseAwaitDraw
	return nil
}

func (g *Game) draw(playerID string) error {
	if err := g.requireTurn(playerID, PhaseAwaitDraw); err != nil {
		return err
	}
	card, ok := g.takeDeckCard()
	if !ok {
		g.end("draw_pile_exhausted")
		return nil
	}
	g.Drawn = card
	g.Phase = PhaseAwaitChoice
	return nil
}

func (g *Game) replace(playerID string, slot int) error {
	if err := g.requireTurn(playerID, PhaseAwaitChoice); err != nil {
		return err
	}
	p := g.player(playerID)
	old, err := occupiedCard(p, slot)
	if err != nil {
		return err
	}
	p.Cards[slot] = g.Drawn
	g.Drawn = nil
	g.openDiscard(old)
	target := CardRef{PlayerID: playerID, Slot: slot}
	g.recordAction("replace", playerID, nil, nil, &target)
	g.finishTurn()
	return nil
}

func (g *Game) discardDrawn(playerID string) error {
	if err := g.requireTurn(playerID, PhaseAwaitChoice); err != nil {
		return err
	}
	card := g.Drawn
	if card == nil {
		return errors.New("no drawn card")
	}
	g.Drawn = nil
	g.openDiscard(card)
	g.ActorID = playerID
	g.recordAction("discard", playerID, nil, nil, nil)
	switch card.Rank {
	case 7, 8:
		g.Phase = PhaseAwaitSelfPeek
	case 9, 10:
		g.Phase = PhaseAwaitOpponentPeek
	case 11, 12:
		g.Phase = PhaseAwaitSwap
	case 13:
		g.Phase = PhaseAwaitKingPeek
	default:
		g.finishTurn()
	}
	return nil
}

func (g *Game) peek(playerID string, target CardRef) error {
	if playerID != g.ActorID || playerID != g.currentPlayerID() {
		return errors.New("only the active player may use this power")
	}
	p := g.player(target.PlayerID)
	card, err := occupiedCard(p, target.Slot)
	if err != nil {
		return err
	}
	kind := ""
	next := Phase("")
	switch g.Phase {
	case PhaseAwaitSelfPeek:
		if target.PlayerID != playerID {
			return errors.New("7 and 8 may only peek at your own card")
		}
		kind, next = "self", PhaseRevealSelf
	case PhaseAwaitOpponentPeek:
		if target.PlayerID == playerID {
			return errors.New("9 and 10 must peek at an opponent card")
		}
		kind, next = "opponent", PhaseRevealOpponent
	case PhaseAwaitKingPeek:
		if target.PlayerID == playerID {
			return errors.New("a King must peek at an opponent card")
		}
		kind, next = "king", PhaseRevealKing
	default:
		return errors.New("no peek is available now")
	}
	g.Reveal = &privateReveal{Kind: kind, Viewer: playerID, Cards: []RevealCard{{Target: target, Card: *card}}}
	g.Phase = next
	return nil
}

func (g *Game) acknowledgeReveal(playerID string) error {
	if g.Reveal == nil || g.Reveal.Viewer != playerID {
		return errors.New("there is no reveal for you to hide")
	}
	g.Reveal = nil
	switch g.Phase {
	case PhaseRevealSelf, PhaseRevealOpponent:
		g.finishTurn()
	case PhaseRevealKing:
		g.Phase = PhaseAwaitSwap
	default:
		return errors.New("reveal is not awaiting acknowledgement")
	}
	return nil
}

func (g *Game) swap(playerID string, first, second CardRef) error {
	if g.Phase != PhaseAwaitSwap || playerID != g.ActorID || playerID != g.currentPlayerID() {
		return errors.New("no swap power is available to you")
	}
	if first == second {
		return errors.New("choose two different occupied cards")
	}
	a := g.player(first.PlayerID)
	b := g.player(second.PlayerID)
	if _, err := occupiedCard(a, first.Slot); err != nil {
		return err
	}
	if _, err := occupiedCard(b, second.Slot); err != nil {
		return err
	}
	a.Cards[first.Slot], b.Cards[second.Slot] = b.Cards[second.Slot], a.Cards[first.Slot]
	g.recordAction("swap", playerID, &first, &second, nil)
	g.finishTurn()
	return nil
}

func (g *Game) slap(playerID string, eventID int, target CardRef) error {
	if g.Phase == PhaseLobby || g.Phase == PhaseInitialPeek || g.Phase == PhaseEnded {
		return errors.New("slaps are not open")
	}
	if len(g.Discard) == 0 || eventID != g.DiscardEventID {
		var card *Card
		if targetPlayer := g.player(target.PlayerID); targetPlayer != nil {
			card, _ = occupiedCard(targetPlayer, target.Slot)
		}
		if card == nil && g.Action != nil && g.Action.Kind == "slap" && g.Action.Target != nil && *g.Action.Target == target {
			card = g.Action.Card
		}
		return g.wrongSlap(playerID, target, card, "that discard is no longer available")
	}
	if g.Phase == PhaseAwaitGift {
		return errors.New("the previous slap gift must be resolved first")
	}
	targetPlayer := g.player(target.PlayerID)
	card, err := occupiedCard(targetPlayer, target.Slot)
	if err != nil {
		return g.wrongSlap(playerID, target, nil, "that slot is empty")
	}
	top := g.Discard[len(g.Discard)-1]
	if card.Rank != top.Rank {
		return g.wrongSlap(playerID, target, card, "ranks do not match")
	}
	targetPlayer.Cards[target.Slot] = nil
	g.openDiscard(card)
	g.recordActionWithCard("slap", playerID, nil, nil, &target, card)
	if target.PlayerID != playerID {
		g.Gift = &pendingGift{SlapperID: playerID, Target: target, Resume: g.Phase}
		g.Phase = PhaseAwaitGift
		return nil
	}
	if countCards(targetPlayer) == 0 {
		g.end("player_has_zero_cards")
	}
	return nil
}

func (g *Game) wrongSlap(playerID string, target CardRef, card *Card, reason string) error {
	g.recordActionWithCard("wrong_slap", playerID, nil, nil, &target, card)
	p := g.player(playerID)
	penaltyCard, ok := g.takeDeckCard()
	if ok {
		p.Cards = append(p.Cards, penaltyCard)
	}
	if !ok {
		g.end("draw_pile_exhausted")
	}
	return actionError{code: "wrong_slap", message: fmt.Sprintf("wrong slap: %s; penalty card added", reason)}
}

func (g *Game) gift(playerID string, sourceSlot int) error {
	if g.Phase != PhaseAwaitGift || g.Gift == nil || g.Gift.SlapperID != playerID {
		return errors.New("you do not have a card to give")
	}
	slapper := g.player(playerID)
	card, err := occupiedCard(slapper, sourceSlot)
	if err != nil {
		return err
	}
	targetPlayer := g.player(g.Gift.Target.PlayerID)
	if targetPlayer == nil || g.Gift.Target.Slot < 0 || g.Gift.Target.Slot >= len(targetPlayer.Cards) || targetPlayer.Cards[g.Gift.Target.Slot] != nil {
		return errors.New("the target slot is no longer empty")
	}
	slapper.Cards[sourceSlot] = nil
	targetPlayer.Cards[g.Gift.Target.Slot] = card
	source := CardRef{PlayerID: playerID, Slot: sourceSlot}
	target := g.Gift.Target
	g.recordAction("gift", playerID, &source, &target, nil)
	resume := g.Gift.Resume
	g.Gift = nil
	g.Phase = resume
	if countCards(slapper) == 0 {
		g.end("player_has_zero_cards")
	}
	return nil
}

func (g *Game) callEnd(playerID string) error {
	if err := g.requireTurn(playerID, PhaseAwaitDraw); err != nil {
		return errors.New("you may call the end only at the start of your turn")
	}
	g.end("called_end")
	return nil
}

func (g *Game) finishTurn() {
	g.ActorID = ""
	g.Reveal = nil
	if len(g.Deck) == 0 {
		g.end("draw_pile_exhausted")
		return
	}
	for _, p := range g.Players {
		if countCards(p) == 0 {
			g.end("player_has_zero_cards")
			return
		}
	}
	g.Current = (g.Current + 1) % len(g.Players)
	g.Phase = PhaseAwaitDraw
}

func (g *Game) end(reason string) {
	g.Phase = PhaseEnded
	g.EndReason = reason
	g.Drawn = nil
	g.Reveal = nil
	g.Gift = nil
	for _, p := range g.Players {
		p.Ready = false
		if !p.Connected {
			p.JoiningNextRound = false
		}
	}
	for _, p := range g.Waiting {
		p.Ready = false
		if !p.Connected {
			p.JoiningNextRound = false
		}
	}
	best := int(^uint(0) >> 1)
	g.WinnerIDs = nil
	g.NextStarterID = ""
	for _, p := range g.Players {
		score := playerScore(p)
		if score < best {
			best = score
			g.WinnerIDs = []string{p.ID}
			g.NextStarterID = p.ID
		} else if score == best {
			g.WinnerIDs = append(g.WinnerIDs, p.ID)
		}
	}
	sort.Strings(g.WinnerIDs)
}

type actionError struct {
	code    string
	message string
}

func (e actionError) Error() string     { return e.message }
func (e actionError) ErrorCode() string { return e.code }

func (g *Game) recordAction(kind, actorID string, first, second, target *CardRef) {
	g.recordActionWithCard(kind, actorID, first, second, target, nil)
}

func (g *Game) recordActionWithCard(kind, actorID string, first, second, target *CardRef, card *Card) {
	g.ActionEventID++
	action := &ActionView{ID: g.ActionEventID, Kind: kind, ActorID: actorID, First: first, Second: second, Target: target}
	if card != nil {
		value := *card
		action.Card = &value
	}
	g.Action = action
}

func (g *Game) View(viewerID string) Snapshot {
	viewer := g.player(viewerID)
	waitingViewer := g.waitingPlayer(viewerID)
	view := Snapshot{
		Type:             "snapshot",
		RoomID:           g.ID,
		YouRole:          MembershipSpectator,
		NextRoundFull:    g.nextRoundCount() >= MaxPlayers,
		AllReady:         g.allNextRoundReady(),
		NextStarterID:    g.NextStarterID,
		NextRoundPlayers: g.nextRoundRoster(),
		WaitingPlayers:   g.waitingRoster(),
		Phase:            g.Phase,
		DrawPileCount:    len(g.Deck),
		HasDrawnCard:     g.Drawn != nil,
		EndReason:        g.EndReason,
		WinnerIDs:        g.WinnerIDs,
	}
	if viewer != nil {
		view.You = Identity{ID: viewer.ID, Name: viewer.Name}
		view.YouRole = MembershipActive
		view.NextRoundJoined = viewer.JoiningNextRound
		view.YouReady = viewer.Ready
	} else if waitingViewer != nil {
		view.You = Identity{ID: waitingViewer.ID, Name: waitingViewer.Name}
		view.NextRoundJoined = waitingViewer.JoiningNextRound
		view.YouReady = waitingViewer.Ready
	}
	if len(g.Players) > 0 && g.Phase != PhaseLobby {
		view.CurrentPlayerID = g.currentPlayerID()
	}
	if len(g.Discard) > 0 {
		card := g.Discard[len(g.Discard)-1]
		view.DiscardTop = &card
		view.DiscardEventID = g.DiscardEventID
	}
	if g.Drawn != nil && viewerID == g.currentPlayerID() {
		card := *g.Drawn
		view.DrawnCard = &card
	}
	if g.Gift != nil {
		view.PendingGift = &GiftView{SlapperID: g.Gift.SlapperID, Target: g.Gift.Target}
	}
	if g.Action != nil {
		action := *g.Action
		view.Action = &action
	}
	if g.Reveal != nil && len(g.Reveal.Cards) > 0 {
		view.PublicPeek = &PublicPeekView{ViewerID: g.Reveal.Viewer, Target: g.Reveal.Cards[0].Target}
	}
	for _, p := range g.Players {
		pv := PlayerView{ID: p.ID, Name: p.Name, Connected: p.Connected}
		if g.Phase == PhaseInitialPeek {
			ready := !g.InitialPending[p.ID]
			pv.InitialReady = &ready
		}
		for slot, hidden := range p.Cards {
			cv := CardSlotView{Slot: slot, Occupied: hidden != nil}
			if hidden != nil && g.Phase == PhaseEnded {
				card := *hidden
				cv.Card = &card
			}
			pv.Cards = append(pv.Cards, cv)
		}
		if g.Phase == PhaseEnded {
			score := playerScore(p)
			pv.Score = &score
		}
		view.Players = append(view.Players, pv)
	}
	if g.Phase == PhaseInitialPeek && g.InitialPending[viewerID] && viewer != nil && len(viewer.Cards) >= 4 {
		view.Reveal = &RevealView{Kind: "initial", Cards: []RevealCard{
			{Target: CardRef{PlayerID: viewerID, Slot: 2}, Card: *viewer.Cards[2]},
			{Target: CardRef{PlayerID: viewerID, Slot: 3}, Card: *viewer.Cards[3]},
		}}
	} else if g.Reveal != nil && g.Reveal.Viewer == viewerID {
		view.Reveal = &RevealView{Kind: g.Reveal.Kind, Cards: append([]RevealCard(nil), g.Reveal.Cards...)}
	}
	return view
}

func (g *Game) nextRoundRoster() []RosterPlayerView {
	roster := make([]RosterPlayerView, 0, MaxPlayers)
	for _, p := range g.Players {
		if p.JoiningNextRound && len(roster) < MaxPlayers {
			roster = append(roster, RosterPlayerView{
				ID:               p.ID,
				Name:             p.Name,
				Connected:        p.Connected,
				JoiningNextRound: true,
				Ready:            p.Ready,
			})
		}
	}
	for _, p := range g.Waiting {
		if p.JoiningNextRound && len(roster) < MaxPlayers {
			roster = append(roster, RosterPlayerView{
				ID:               p.ID,
				Name:             p.Name,
				Connected:        p.Connected,
				JoiningNextRound: true,
				Ready:            p.Ready,
			})
		}
	}
	return roster
}

func (g *Game) waitingRoster() []RosterPlayerView {
	players := make([]RosterPlayerView, 0, len(g.Waiting))
	for _, p := range g.Waiting {
		players = append(players, RosterPlayerView{
			ID:               p.ID,
			Name:             p.Name,
			Connected:        p.Connected,
			JoiningNextRound: p.JoiningNextRound,
			Ready:            p.Ready,
		})
	}
	return players
}

func (g *Game) requireTurn(playerID string, phase Phase) error {
	if g.Phase != phase {
		return fmt.Errorf("action is not available during %s", g.Phase)
	}
	if playerID != g.currentPlayerID() {
		return errors.New("it is not your turn")
	}
	return nil
}

func (g *Game) currentPlayerID() string {
	if len(g.Players) == 0 || g.Current < 0 || g.Current >= len(g.Players) {
		return ""
	}
	return g.Players[g.Current].ID
}

func (g *Game) player(id string) *Player {
	for _, p := range g.Players {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (g *Game) waitingPlayer(id string) *WaitingPlayer {
	for _, p := range g.Waiting {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (g *Game) openDiscard(card *Card) {
	if card == nil {
		return
	}
	g.Discard = append(g.Discard, *card)
	g.DiscardEventID++
}

func (g *Game) takeDeckCard() (*Card, bool) {
	if len(g.Deck) == 0 {
		return nil, false
	}
	last := len(g.Deck) - 1
	card := g.Deck[last]
	g.Deck = g.Deck[:last]
	return &card, true
}

func occupiedCard(p *Player, slot int) (*Card, error) {
	if p == nil {
		return nil, errors.New("unknown player")
	}
	if slot < 0 || slot >= len(p.Cards) {
		return nil, errors.New("invalid card slot")
	}
	if p.Cards[slot] == nil {
		return nil, errors.New("card slot is empty")
	}
	return p.Cards[slot], nil
}

func countCards(p *Player) int {
	n := 0
	for _, card := range p.Cards {
		if card != nil {
			n++
		}
	}
	return n
}

func CardScore(card Card) int {
	if card.Rank == 0 {
		return 0
	}
	if card.Rank == 13 && (card.Suit == Hearts || card.Suit == Diamonds) {
		return -1
	}
	return card.Rank
}

func playerScore(p *Player) int {
	total := 0
	for _, card := range p.Cards {
		if card != nil {
			total += CardScore(*card)
		}
	}
	return total
}

func newDeck() []Card {
	deck := make([]Card, 0, 54)
	for _, suit := range []Suit{Clubs, Diamonds, Hearts, Spades} {
		for rank := 1; rank <= 13; rank++ {
			deck = append(deck, Card{ID: fmt.Sprintf("%s-%d", suit, rank), Rank: rank, Suit: suit})
		}
	}
	deck = append(deck,
		Card{ID: "joker-1", Rank: 0, Suit: Joker},
		Card{ID: "joker-2", Rank: 0, Suit: Joker},
	)
	return deck
}
