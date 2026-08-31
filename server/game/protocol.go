package game

import "encoding/json"

type Suit string

const (
	Clubs    Suit = "clubs"
	Diamonds Suit = "diamonds"
	Hearts   Suit = "hearts"
	Spades   Suit = "spades"
	Joker    Suit = "joker"
)

type Card struct {
	ID   string `json:"id"`
	Rank int    `json:"rank"`
	Suit Suit   `json:"suit"`
}

type CardRef struct {
	PlayerID string `json:"playerId"`
	Slot     int    `json:"slot"`
}

type ClientMessage struct {
	Type          string  `json:"type"`
	Slot          int     `json:"slot"`
	Target        CardRef `json:"target"`
	First         CardRef `json:"first"`
	Second        CardRef `json:"second"`
	EventID       int     `json:"eventId"`
	SourceSlot    int     `json:"sourceSlot"`
	JoinNextRound bool    `json:"joinNextRound"`
}

type CardSlotView struct {
	Slot     int   `json:"slot"`
	Occupied bool  `json:"occupied"`
	Card     *Card `json:"card,omitempty"`
}

type PlayerView struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Connected    bool           `json:"connected"`
	Cards        []CardSlotView `json:"cards"`
	InitialReady *bool          `json:"initialReady,omitempty"`
	Score        *int           `json:"score,omitempty"`
}

type RosterPlayerView struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Connected        bool   `json:"connected"`
	JoiningNextRound bool   `json:"joiningNextRound"`
}

type RevealCard struct {
	Target CardRef `json:"target"`
	Card   Card    `json:"card"`
}

type RevealView struct {
	Kind  string       `json:"kind"`
	Cards []RevealCard `json:"cards"`
}

type GiftView struct {
	SlapperID string  `json:"slapperId"`
	Target    CardRef `json:"target"`
}

type PublicPeekView struct {
	ViewerID string  `json:"viewerId"`
	Target   CardRef `json:"target"`
}

type ActionView struct {
	ID      int      `json:"id"`
	Kind    string   `json:"kind"`
	ActorID string   `json:"actorId"`
	First   *CardRef `json:"first,omitempty"`
	Second  *CardRef `json:"second,omitempty"`
	Target  *CardRef `json:"target,omitempty"`
}

type Snapshot struct {
	Type             string             `json:"type"`
	RoomID           string             `json:"roomId"`
	You              Identity           `json:"you"`
	YouRole          string             `json:"youRole"`
	NextRoundJoined  bool               `json:"nextRoundJoined"`
	NextRoundFull    bool               `json:"nextRoundFull"`
	NextRoundPlayers []RosterPlayerView `json:"nextRoundPlayers"`
	WaitingPlayers   []RosterPlayerView `json:"waitingPlayers"`
	Phase            Phase              `json:"phase"`
	CurrentPlayerID  string             `json:"currentPlayerId,omitempty"`
	Players          []PlayerView       `json:"players"`
	DrawPileCount    int                `json:"drawPileCount"`
	HasDrawnCard     bool               `json:"hasDrawnCard"`
	DiscardTop       *Card              `json:"discardTop,omitempty"`
	DiscardEventID   int                `json:"discardEventId,omitempty"`
	DrawnCard        *Card              `json:"drawnCard,omitempty"`
	Reveal           *RevealView        `json:"reveal,omitempty"`
	PublicPeek       *PublicPeekView    `json:"publicPeek,omitempty"`
	Action           *ActionView        `json:"action,omitempty"`
	PendingGift      *GiftView          `json:"pendingGift,omitempty"`
	EndReason        string             `json:"endReason,omitempty"`
	WinnerIDs        []string           `json:"winnerIds,omitempty"`
}

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ErrorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func EncodeError(code, message string) []byte {
	b, _ := json.Marshal(ErrorMessage{Type: "error", Code: code, Message: message})
	return b
}
