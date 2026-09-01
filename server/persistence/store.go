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
	_, err = tx.Exec(`
		INSERT INTO rounds (room_id, round_number, started_at, ended_at, end_reason, called_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (room_id, round_number) DO UPDATE SET
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			end_reason = excluded.end_reason,
			called_by = excluded.called_by
	`, result.RoomID, result.Round, timestamp(result.StartedAt), timestamp(result.EndedAt), result.EndReason, result.CalledBy)
	if err != nil {
		return err
	}

	var roundID int64
	if err := tx.QueryRow(`SELECT id FROM rounds WHERE room_id = ? AND round_number = ?`, result.RoomID, result.Round).Scan(&roundID); err != nil {
		return err
	}
	for _, player := range result.Players {
		_, err = tx.Exec(`
			INSERT INTO round_players (round_id, player_id, display_name, score, is_winner, is_loser, called_kabo, kabo_failed)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (round_id, player_id) DO UPDATE SET
				display_name = excluded.display_name,
				score = excluded.score,
				is_winner = excluded.is_winner,
				is_loser = excluded.is_loser,
				called_kabo = excluded.called_kabo,
				kabo_failed = excluded.kabo_failed
		`, roundID, player.ID, player.Name, player.Score, boolInt(player.Winner), boolInt(player.Loser), boolInt(player.CalledKabo), boolInt(player.KaboFailed))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
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
    round_number INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    end_reason TEXT NOT NULL,
    called_by TEXT NOT NULL DEFAULT '',
    UNIQUE (room_id, round_number)
);

CREATE TABLE IF NOT EXISTS round_players (
    round_id INTEGER NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    player_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    score INTEGER NOT NULL,
    is_winner INTEGER NOT NULL CHECK (is_winner IN (0, 1)),
    is_loser INTEGER NOT NULL DEFAULT 0 CHECK (is_loser IN (0, 1)),
    called_kabo INTEGER NOT NULL CHECK (called_kabo IN (0, 1)),
    kabo_failed INTEGER NOT NULL CHECK (kabo_failed IN (0, 1)),
    PRIMARY KEY (round_id, player_id)
);

CREATE INDEX IF NOT EXISTS round_players_score_idx ON round_players (score);
CREATE INDEX IF NOT EXISTS round_players_winner_idx ON round_players (is_winner);
`

func ensureRoundPlayerColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(round_players)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	hasLoser := false
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
		if name == "is_loser" {
			hasLoser = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasLoser {
		return nil
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE round_players ADD COLUMN is_loser INTEGER NOT NULL DEFAULT 0 CHECK (is_loser IN (0, 1))`)
	return err
}
