package persistence

import (
	"testing"
	"time"

	"kabo/server/game"
)

func TestRecordRoundIsIdempotentAndStoresPlayers(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	result := game.RoundResult{
		RoomID:    "room",
		Round:     3,
		StartedAt: time.Unix(10, 0),
		EndedAt:   time.Unix(20, 0),
		EndReason: "called_end",
		CalledBy:  "b",
		Players: []game.PlayerResult{
			{ID: "a", Name: "Ada", Score: 4, Winner: true},
			{ID: "b", Name: "Ben", Score: 9, Loser: true, CalledKabo: true, KaboFailed: true},
		},
	}
	if err := store.RecordRound(result); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRound(result); err != nil {
		t.Fatal(err)
	}

	var rounds, players int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rounds`).Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM round_players`).Scan(&players); err != nil {
		t.Fatal(err)
	}
	if rounds != 1 || players != 2 {
		t.Fatalf("stored rows = rounds:%d players:%d, want 1 and 2", rounds, players)
	}

	var loser, failed int
	if err := store.db.QueryRow(`SELECT is_loser, kabo_failed FROM round_players WHERE player_id = 'b'`).Scan(&loser, &failed); err != nil {
		t.Fatal(err)
	}
	if loser != 1 || failed != 1 {
		t.Fatalf("failed Kabo result was not stored: loser=%d failed=%d", loser, failed)
	}
}
