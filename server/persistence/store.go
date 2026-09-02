package persistence

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"kabo/server/game"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type LeaderboardEntry struct {
	PlayerID    string
	DisplayName string
	Games       int
	Wins        int
	TotalScore  int
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRoundPlayerColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureRoundColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS rounds_guild_idx ON rounds (guild_id);
		CREATE INDEX IF NOT EXISTS rounds_instance_idx ON rounds (instance_id);
		CREATE INDEX IF NOT EXISTS rounds_started_idx ON rounds (started_at);
		CREATE INDEX IF NOT EXISTS round_events_kind_idx ON round_events (kind)
	`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) RecordRound(result game.RoundResult) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	if result.Round <= 0 || result.RoomID == "" || len(result.Players) == 0 {
		return errors.New("round result is incomplete")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	instanceID := result.InstanceID
	if instanceID == "" {
		instanceID = result.RoomID
	}
	durationMS := int64(0)
	if result.EndedAt.After(result.StartedAt) {
		durationMS = result.EndedAt.Sub(result.StartedAt).Milliseconds()
	}
	_, err = tx.Exec(`
		INSERT INTO rounds (
			room_id, platform, application_id, instance_id, guild_id, channel_id,
			location_id, custom_id, referrer_id, round_number, player_count,
			event_count, started_at, ended_at, duration_ms, end_reason, called_by
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (room_id, round_number) DO UPDATE SET
			platform = excluded.platform,
			application_id = excluded.application_id,
			instance_id = excluded.instance_id,
			guild_id = excluded.guild_id,
			channel_id = excluded.channel_id,
			location_id = excluded.location_id,
			custom_id = excluded.custom_id,
			referrer_id = excluded.referrer_id,
			player_count = excluded.player_count,
			event_count = excluded.event_count,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			duration_ms = excluded.duration_ms,
			end_reason = excluded.end_reason,
			called_by = excluded.called_by
	`, result.RoomID, result.Platform, result.ApplicationID, instanceID, result.GuildID, result.ChannelID,
		result.LocationID, result.CustomID, result.ReferrerID, result.Round, len(result.Players), len(result.Events),
		timestamp(result.StartedAt), timestamp(result.EndedAt), durationMS, result.EndReason, result.CalledBy)
	if err != nil {
		return err
	}

	var roundID int64
	if err := tx.QueryRow(`SELECT id FROM rounds WHERE room_id = ? AND round_number = ?`, result.RoomID, result.Round).Scan(&roundID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM round_players WHERE round_id = ?`, roundID); err != nil {
		return err
	}
	for _, player := range result.Players {
		_, err = tx.Exec(`
			INSERT INTO round_players (
				round_id, player_id, display_name, seat_index, card_count, connected,
				score, is_winner, is_loser, called_kabo, kabo_failed
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, roundID, player.ID, player.Name, player.Seat, player.CardCount, boolInt(player.Connected), player.Score,
			boolInt(player.Winner), boolInt(player.Loser), boolInt(player.CalledKabo), boolInt(player.KaboFailed))
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM round_events WHERE round_id = ?`, roundID); err != nil {
		return err
	}
	for _, event := range result.Events {
		_, err = tx.Exec(`
			INSERT INTO round_events (
				round_id, sequence, occurred_at, kind, actor_id,
				first_player_id, first_slot, second_player_id, second_slot,
				target_player_id, target_slot, card_id, card_rank, card_suit, reason
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, roundID, event.Sequence, timestamp(event.At), event.Kind, event.ActorID,
			refPlayer(event.First), refSlot(event.First), refPlayer(event.Second), refSlot(event.Second),
			refPlayer(event.Target), refSlot(event.Target), cardID(event.Card), cardRank(event.Card), cardSuit(event.Card), event.Reason)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Leaderboard(guildID string, limit int) ([]LeaderboardEntry, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	if guildID == "" {
		return nil, errors.New("guild ID is empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}

	rows, err := s.db.Query(`
		SELECT stats.player_id,
			COALESCE((
				SELECT latest.display_name
				FROM round_players latest
				JOIN rounds latest_round ON latest_round.id = latest.round_id
				WHERE latest.player_id = stats.player_id
					AND latest_round.platform = 'discord'
					AND latest_round.guild_id = ?
				ORDER BY latest_round.ended_at DESC, latest_round.id DESC
				LIMIT 1
			), '') AS display_name,
			stats.games,
			stats.wins,
			stats.total_score
		FROM (
			SELECT rp.player_id,
				COUNT(*) AS games,
				SUM(rp.is_winner) AS wins,
				SUM(rp.score) AS total_score
			FROM round_players rp
			JOIN rounds r ON r.id = rp.round_id
			WHERE r.platform = 'discord' AND r.guild_id = ?
			GROUP BY rp.player_id
		) stats
		ORDER BY stats.total_score ASC, stats.wins DESC, stats.games DESC,
			COALESCE(display_name, stats.player_id) COLLATE NOCASE ASC
		LIMIT ?
	`, guildID, guildID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]LeaderboardEntry, 0, limit)
	for rows.Next() {
		var entry LeaderboardEntry
		if err := rows.Scan(&entry.PlayerID, &entry.DisplayName, &entry.Games, &entry.Wins, &entry.TotalScore); err != nil {
			return nil, err
		}
		if entry.DisplayName == "" {
			entry.DisplayName = entry.PlayerID
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func refPlayer(ref *game.CardRef) any {
	if ref == nil {
		return nil
	}
	return ref.PlayerID
}

func refSlot(ref *game.CardRef) any {
	if ref == nil {
		return nil
	}
	return ref.Slot
}

func cardID(card *game.Card) any {
	if card == nil {
		return nil
	}
	return card.ID
}

func cardRank(card *game.Card) any {
	if card == nil {
		return nil
	}
	return card.Rank
}

func cardSuit(card *game.Card) any {
	if card == nil {
		return nil
	}
	return string(card.Suit)
}

func timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

const schema = `
CREATE TABLE IF NOT EXISTS rounds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    room_id TEXT NOT NULL,
    platform TEXT NOT NULL DEFAULT '',
    application_id TEXT NOT NULL DEFAULT '',
    instance_id TEXT NOT NULL DEFAULT '',
    guild_id TEXT NOT NULL DEFAULT '',
    channel_id TEXT NOT NULL DEFAULT '',
    location_id TEXT NOT NULL DEFAULT '',
    custom_id TEXT NOT NULL DEFAULT '',
    referrer_id TEXT NOT NULL DEFAULT '',
    round_number INTEGER NOT NULL,
    player_count INTEGER NOT NULL DEFAULT 0,
    event_count INTEGER NOT NULL DEFAULT 0,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    end_reason TEXT NOT NULL,
    called_by TEXT NOT NULL DEFAULT '',
    UNIQUE (room_id, round_number)
);

CREATE TABLE IF NOT EXISTS round_players (
    round_id INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    player_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    seat_index INTEGER NOT NULL DEFAULT -1,
    card_count INTEGER NOT NULL DEFAULT 0,
    connected INTEGER NOT NULL DEFAULT 1 CHECK (connected IN (0, 1)),
    score INTEGER NOT NULL,
    is_winner INTEGER NOT NULL CHECK (is_winner IN (0, 1)),
    is_loser INTEGER NOT NULL DEFAULT 0 CHECK (is_loser IN (0, 1)),
    called_kabo INTEGER NOT NULL CHECK (called_kabo IN (0, 1)),
    kabo_failed INTEGER NOT NULL CHECK (kabo_failed IN (0, 1)),
    PRIMARY KEY (round_id, player_id)
);

CREATE INDEX IF NOT EXISTS round_players_score_idx ON round_players (score);
CREATE INDEX IF NOT EXISTS round_players_winner_idx ON round_players (is_winner);

CREATE TABLE IF NOT EXISTS round_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    round_id INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    occurred_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    actor_id TEXT NOT NULL DEFAULT '',
    first_player_id TEXT,
    first_slot INTEGER,
    second_player_id TEXT,
    second_slot INTEGER,
    target_player_id TEXT,
    target_slot INTEGER,
    card_id TEXT,
    card_rank INTEGER,
    card_suit TEXT,
    reason TEXT NOT NULL DEFAULT '',
    UNIQUE (round_id, sequence)
);
`

func ensureRoundPlayerColumns(db *sql.DB) error {
	return ensureTableColumns(db, "round_players", []tableColumn{
		{name: "is_loser", definition: "INTEGER NOT NULL DEFAULT 0 CHECK (is_loser IN (0, 1))"},
		{name: "seat_index", definition: "INTEGER NOT NULL DEFAULT -1"},
		{name: "card_count", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "connected", definition: "INTEGER NOT NULL DEFAULT 1 CHECK (connected IN (0, 1))"},
	})
}

func ensureRoundColumns(db *sql.DB) error {
	if err := ensureTableColumns(db, "rounds", []tableColumn{
		{name: "platform", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "application_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "instance_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "guild_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "channel_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "location_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "custom_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "referrer_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "player_count", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "event_count", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "duration_ms", definition: "INTEGER NOT NULL DEFAULT 0"},
	}); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE rounds SET instance_id = room_id WHERE instance_id = ''`); err != nil {
		return err
	}
	_, err := db.Exec(`
		UPDATE rounds
		SET player_count = (
			SELECT COUNT(*) FROM round_players WHERE round_players.round_id = rounds.id
		)
		WHERE player_count = 0
	`)
	return err
}

type tableColumn struct {
	name       string
	definition string
}

func ensureTableColumns(db *sql.DB, table string, columns []tableColumn) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
			return err
		}
	}
	return nil
}
