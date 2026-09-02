package persistence

import (
	"database/sql"
	"fmt"
	"path/filepath"
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
		RoomID:         "room",
		Platform:       "discord",
		ClientPlatform: "desktop",
		ApplicationID:  "app-123",
		InstanceID:     "instance-123",
		GuildID:        "guild-123",
		ChannelID:      "channel-123",
		LocationID:     "guild",
		CustomID:       "launch",
		ReferrerID:     "referrer",
		Round:          3,
		StartedAt:      time.Unix(10, 0),
		EndedAt:        time.Unix(20, 0),
		EndReason:      "called_end",
		CalledBy:       "b",
		Players: []game.PlayerResult{
			{ID: "a", Name: "Ada", Seat: 0, CardCount: 2, Connected: true, Score: 4, Winner: true},
			{ID: "b", Name: "Ben", Seat: 1, CardCount: 3, Connected: true, Score: 9, Loser: true, CalledKabo: true, KaboFailed: true},
		},
		Events: []game.RoundEvent{
			{
				Sequence: 1,
				At:       time.Unix(15, 0),
				Kind:     "slap",
				ActorID:  "b",
				Target:   &game.CardRef{PlayerID: "a", Slot: 2},
				Card:     &game.Card{ID: "card-5", Rank: 5, Suit: game.Hearts},
			},
		},
	}
	if err := store.RecordRound(result); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRound(result); err != nil {
		t.Fatal(err)
	}

	var rounds, players, events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM rounds`).Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM round_players`).Scan(&players); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM round_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if rounds != 1 || players != 2 || events != 1 {
		t.Fatalf("stored rows = rounds:%d players:%d events:%d, want 1, 2, and 1", rounds, players, events)
	}

	var loser, failed int
	if err := store.db.QueryRow(`SELECT is_loser, kabo_failed FROM round_players WHERE player_id = 'b'`).Scan(&loser, &failed); err != nil {
		t.Fatal(err)
	}
	if loser != 1 || failed != 1 {
		t.Fatalf("failed Kabo result was not stored: loser=%d failed=%d", loser, failed)
	}

	var platform, clientPlatform, applicationID, instanceID, guildID, channelID, locationID, customID, referrerID string
	var playerCount, eventCount, durationMS int64
	if err := store.db.QueryRow(`
		SELECT platform, client_platform, application_id, instance_id, guild_id, channel_id, location_id,
			custom_id, referrer_id, player_count, event_count, duration_ms
		FROM rounds WHERE room_id = 'room' AND round_number = 3
	`).Scan(&platform, &clientPlatform, &applicationID, &instanceID, &guildID, &channelID, &locationID, &customID, &referrerID, &playerCount, &eventCount, &durationMS); err != nil {
		t.Fatal(err)
	}
	if platform != "discord" || clientPlatform != "desktop" || applicationID != "app-123" || instanceID != "instance-123" || guildID != "guild-123" || channelID != "channel-123" || locationID != "guild" || customID != "launch" || referrerID != "referrer" || playerCount != 2 || eventCount != 1 || durationMS != 10000 {
		t.Fatalf("round metadata was not stored: platform=%q client=%q app=%q instance=%q guild=%q channel=%q location=%q custom=%q referrer=%q players=%d events=%d duration=%d", platform, clientPlatform, applicationID, instanceID, guildID, channelID, locationID, customID, referrerID, playerCount, eventCount, durationMS)
	}

	var seat, cardCount, connected int
	if err := store.db.QueryRow(`SELECT seat_index, card_count, connected FROM round_players WHERE player_id = 'b'`).Scan(&seat, &cardCount, &connected); err != nil {
		t.Fatal(err)
	}
	if seat != 1 || cardCount != 3 || connected != 1 {
		t.Fatalf("player metadata was not stored: seat=%d cards=%d connected=%d", seat, cardCount, connected)
	}

	var kind, actorID, targetPlayerID, cardID, cardSuit string
	var targetSlot, cardRank int
	if err := store.db.QueryRow(`
		SELECT kind, actor_id, target_player_id, target_slot, card_id, card_rank, card_suit
		FROM round_events WHERE round_id = (SELECT id FROM rounds WHERE room_id = 'room' AND round_number = 3)
	`).Scan(&kind, &actorID, &targetPlayerID, &targetSlot, &cardID, &cardRank, &cardSuit); err != nil {
		t.Fatal(err)
	}
	if kind != "slap" || actorID != "b" || targetPlayerID != "a" || targetSlot != 2 || cardID != "card-5" || cardRank != 5 || cardSuit != "hearts" {
		t.Fatalf("event details were not stored: kind=%q actor=%q target=%q:%d card=%q/%d/%q", kind, actorID, targetPlayerID, targetSlot, cardID, cardRank, cardSuit)
	}
}

func TestLeaderboardIsGuildScopedAndRanksByTotalWins(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	results := []game.RoundResult{
		{
			RoomID: "guild-a-room", Platform: "discord", GuildID: "guild-a", Round: 1,
			StartedAt: time.Unix(10, 0), EndedAt: time.Unix(20, 0), EndReason: "called_end",
			Players: []game.PlayerResult{
				{ID: "a", Name: "Ada", Score: 4, Winner: true},
				{ID: "b", Name: "Ben", Score: 3},
			},
		},
		{
			RoomID: "guild-a-room", Platform: "discord", GuildID: "guild-a", Round: 2,
			StartedAt: time.Unix(30, 0), EndedAt: time.Unix(40, 0), EndReason: "called_end",
			Players: []game.PlayerResult{
				{ID: "a", Name: "Ada Updated", Score: 3},
				{ID: "b", Name: "Ben", Score: 1, Winner: true},
			},
		},
		{
			RoomID: "guild-a-room", Platform: "discord", GuildID: "guild-a", Round: 3,
			StartedAt: time.Unix(45, 0), EndedAt: time.Unix(46, 0), EndReason: "called_end",
			Players: []game.PlayerResult{
				{ID: "a", Name: "Ada Updated", Score: 12, Winner: true},
				{ID: "b", Name: "Ben", Score: 0},
			},
		},
		{
			RoomID: "guild-b-room", Platform: "discord", GuildID: "guild-b", Round: 1,
			StartedAt: time.Unix(50, 0), EndedAt: time.Unix(60, 0), EndReason: "called_end",
			Players: []game.PlayerResult{
				{ID: "a", Name: "Ada Other Server", Score: 0, Winner: true},
			},
		},
	}
	for _, result := range results {
		if err := store.RecordRound(result); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := store.Leaderboard("guild-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("leaderboard returned %d players, want 2", len(entries))
	}
	if entries[0].PlayerID != "a" || entries[0].DisplayName != "Ada Updated" || entries[0].Games != 3 || entries[0].Wins != 2 || entries[0].TotalScore != 19 {
		t.Fatalf("first leaderboard entry = %+v, want Ada with 2 wins despite her higher hand-score total", entries[0])
	}
	if entries[1].PlayerID != "b" || entries[1].DisplayName != "Ben" || entries[1].Games != 3 || entries[1].Wins != 1 || entries[1].TotalScore != 4 {
		t.Fatalf("second leaderboard entry = %+v, want Ben with 1 win despite his lower hand-score total", entries[1])
	}
}

func TestLeaderboardPageReturnsAbsoluteSliceAndTotal(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	players := make([]game.PlayerResult, 12)
	for index := range players {
		players[index] = game.PlayerResult{
			ID:     fmt.Sprintf("player-%02d", index+1),
			Name:   fmt.Sprintf("Player %02d", index+1),
			Score:  index,
			Winner: index == 0,
		}
	}
	if err := store.RecordRound(game.RoundResult{
		RoomID: "pagination", Platform: "discord", GuildID: "guild", Round: 1,
		StartedAt: time.Unix(10, 0), EndedAt: time.Unix(20, 0), EndReason: "called_end", Players: players,
	}); err != nil {
		t.Fatal(err)
	}

	page, total, err := store.LeaderboardPage("guild", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 || len(page) != 2 {
		t.Fatalf("page length = %d total = %d, want 2/12", len(page), total)
	}
	if page[0].DisplayName != "Player 11" || page[1].DisplayName != "Player 12" {
		t.Fatalf("second page = %+v", page)
	}
}

func TestOpenMigratesDiscordClientPlatformForLeaderboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform-migration.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRound(game.RoundResult{
		RoomID: "activity-room", Platform: "desktop", ApplicationID: "app", GuildID: "guild", Round: 1,
		StartedAt: time.Unix(10, 0), EndedAt: time.Unix(20, 0), EndReason: "called_end",
		Players: []game.PlayerResult{{ID: "player", Name: "Player", Score: 4, Winner: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var platform, clientPlatform string
	if err := store.db.QueryRow(`
		SELECT platform, client_platform FROM rounds WHERE room_id = 'activity-room'
	`).Scan(&platform, &clientPlatform); err != nil {
		t.Fatal(err)
	}
	if platform != "discord" || clientPlatform != "desktop" {
		t.Fatalf("migrated platform = %q client = %q, want discord/desktop", platform, clientPlatform)
	}
	entries, err := store.Leaderboard("guild", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].PlayerID != "player" {
		t.Fatalf("migrated leaderboard = %+v, want saved Activity player", entries)
	}
}

func TestOpenMigratesLegacyRoundTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE rounds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			room_id TEXT NOT NULL,
			round_number INTEGER NOT NULL,
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL,
			end_reason TEXT NOT NULL,
			called_by TEXT NOT NULL DEFAULT '',
			UNIQUE (room_id, round_number)
		);
		CREATE TABLE round_players (
			round_id INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
			player_id TEXT NOT NULL,
			display_name TEXT NOT NULL,
			score INTEGER NOT NULL,
			is_winner INTEGER NOT NULL CHECK (is_winner IN (0, 1)),
			called_kabo INTEGER NOT NULL CHECK (called_kabo IN (0, 1)),
			kabo_failed INTEGER NOT NULL CHECK (kabo_failed IN (0, 1)),
			PRIMARY KEY (round_id, player_id)
		);
	`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	result := game.RoundResult{
		RoomID:    "legacy-room",
		Round:     1,
		StartedAt: time.Unix(10, 0),
		EndedAt:   time.Unix(11, 0),
		EndReason: "called_end",
		Players: []game.PlayerResult{
			{ID: "legacy-player", Name: "Legacy", Connected: true, Winner: true},
		},
	}
	if err := store.RecordRound(result); err != nil {
		t.Fatal(err)
	}

	var instanceID string
	var playerCount int
	if err := store.db.QueryRow(`SELECT instance_id, player_count FROM rounds WHERE room_id = 'legacy-room'`).Scan(&instanceID, &playerCount); err != nil {
		t.Fatal(err)
	}
	if instanceID != "legacy-room" || playerCount != 1 {
		t.Fatalf("legacy round was not migrated: instance=%q players=%d", instanceID, playerCount)
	}
}
