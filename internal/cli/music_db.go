// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — music_db.go
// Local SQLite store for the Music library synced from Music.app via JXA. Holds
// one row per library track plus an FTS5 index over name/artist/album.
package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type musicStore struct {
	db *sql.DB
}

func musicDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-pp-cli", "music.db")
}

func openMusicStore() (*musicStore, error) {
	path := musicDBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating music db dir: %w", err)
	}
	db, err := sql.Open("sqlite",
		path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON&_temp_store=MEMORY",
	)
	if err != nil {
		return nil, fmt.Errorf("opening music db: %w", err)
	}
	db.SetMaxOpenConns(4)
	s := &musicStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("music db migrate: %w", err)
	}
	return s, nil
}

func (s *musicStore) Close() error { return s.db.Close() }

func (s *musicStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tracks (
			id           TEXT PRIMARY KEY,
			name         TEXT,
			artist       TEXT,
			album        TEXT,
			album_artist TEXT,
			genre        TEXT,
			year         INTEGER,
			duration_sec INTEGER,
			play_count   INTEGER,
			rating       INTEGER,
			loved        INTEGER DEFAULT 0,
			date_added   TEXT,
			synced_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_plays  ON tracks(play_count)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS tracks_fts USING fts5(id UNINDEXED, body, tokenize='unicode61')`,
		`CREATE TABLE IF NOT EXISTS music_sync_state (key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt[:min(60, len(stmt))])
		}
	}
	return nil
}

// jxaTrack is the JSON shape emitted by the JXA sync script.
type jxaTrack struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist"`
	Genre       string `json:"genre"`
	Year        int    `json:"year"`
	Duration    int    `json:"duration"`
	PlayCount   int    `json:"playCount"`
	Rating      int    `json:"rating"`
	Loved       bool   `json:"loved"`
	DateAdded   string `json:"dateAdded"`
}

func (s *musicStore) SyncAll(tracks []jxaTrack) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM tracks"); err != nil {
		return 0, fmt.Errorf("sync: clear tracks: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM tracks_fts"); err != nil {
		return 0, fmt.Errorf("sync: clear fts: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, t := range tracks {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO tracks (id, name, artist, album, album_artist, genre, year, duration_sec, play_count, rating, loved, date_added, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Name, t.Artist, t.Album, t.AlbumArtist, t.Genre, t.Year,
			t.Duration, t.PlayCount, t.Rating, boolToInt(t.Loved), t.DateAdded, now,
		); err != nil {
			return 0, fmt.Errorf("sync: insert track: %w", err)
		}
		body := strings.Join([]string{t.Name, t.Artist, t.Album, t.Genre}, " ")
		if _, err := tx.Exec(`INSERT INTO tracks_fts (id, body) VALUES (?, ?)`, t.ID, body); err != nil {
			return 0, fmt.Errorf("sync: insert fts: %w", err)
		}
		count++
	}
	if _, err := tx.Exec(
		`INSERT INTO music_sync_state (key, value, updated_at) VALUES ('last_sync', ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, now,
	); err != nil {
		return 0, fmt.Errorf("sync: record state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// Track is a row from the tracks table.
type Track struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Year        int    `json:"year,omitempty"`
	DurationSec int    `json:"duration_sec"`
	PlayCount   int    `json:"play_count"`
	Rating      int    `json:"rating,omitempty"`
	Loved       bool   `json:"loved"`
	DateAdded   string `json:"date_added,omitempty"`
}

func (s *musicStore) Count() (int, error) {
	var n int
	return n, s.db.QueryRow("SELECT COUNT(*) FROM tracks").Scan(&n)
}

func (s *musicStore) LastSyncedAt() string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM music_sync_state WHERE key = 'last_sync'").Scan(&v)
	return v
}

// List returns tracks ordered by the given sort key: "plays" (default),
// "recent" (date added), or "name".
func (s *musicStore) List(sortKey, artist string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 50
	}
	order := "play_count DESC"
	switch sortKey {
	case "recent":
		order = "date_added DESC"
	case "name":
		order = "name COLLATE NOCASE ASC"
	}
	var where string
	var args []any
	if artist != "" {
		where = "WHERE LOWER(artist) LIKE ?"
		args = append(args, "%"+strings.ToLower(artist)+"%")
	}
	q := fmt.Sprintf(
		`SELECT id, name, artist, album, genre, year, duration_sec, play_count, rating, loved, date_added
		 FROM tracks %s ORDER BY %s LIMIT %d`, where, order, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracks(rows)
}

func (s *musicStore) Search(query string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT t.id, t.name, t.artist, t.album, t.genre, t.year, t.duration_sec, t.play_count, t.rating, t.loved, t.date_added
		 FROM tracks_fts f JOIN tracks t ON t.id = f.id
		 WHERE tracks_fts MATCH ? ORDER BY t.play_count DESC LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTracks(rows)
}

func scanTracks(rows *sql.Rows) ([]Track, error) {
	var out []Track
	for rows.Next() {
		var t Track
		var name, artist, album, genre, date sql.NullString
		var year, dur, plays, rating, loved sql.NullInt64
		if err := rows.Scan(&t.ID, &name, &artist, &album, &genre, &year, &dur, &plays, &rating, &loved, &date); err != nil {
			return nil, err
		}
		t.Name, t.Artist, t.Album, t.Genre = name.String, artist.String, album.String, genre.String
		t.Year, t.DurationSec, t.PlayCount, t.Rating = int(year.Int64), int(dur.Int64), int(plays.Int64), int(rating.Int64)
		t.Loved = loved.Int64 == 1
		t.DateAdded = date.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// ── analytics ───────────────────────────────────────────────────────────────

// ArtistStat ranks an artist by track count and total plays.
type ArtistStat struct {
	Artist string `json:"artist"`
	Tracks int64  `json:"tracks"`
	Plays  int64  `json:"plays"`
}

func (s *musicStore) TopArtists(limit int) ([]ArtistStat, error) {
	if limit <= 0 {
		limit = 15
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT COALESCE(NULLIF(artist, ''), '(unknown)'), COUNT(*), COALESCE(SUM(play_count), 0)
		FROM tracks GROUP BY artist ORDER BY SUM(play_count) DESC, COUNT(*) DESC LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistStat
	for rows.Next() {
		var a ArtistStat
		if err := rows.Scan(&a.Artist, &a.Tracks, &a.Plays); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GenreStat ranks a genre by track count.
type GenreStat struct {
	Genre  string `json:"genre"`
	Tracks int64  `json:"tracks"`
}

func (s *musicStore) TopGenres(limit int) ([]GenreStat, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT COALESCE(NULLIF(genre, ''), '(none)'), COUNT(*)
		FROM tracks GROUP BY genre ORDER BY COUNT(*) DESC LIMIT %d`, limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenreStat
	for rows.Next() {
		var g GenreStat
		if err := rows.Scan(&g.Genre, &g.Tracks); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MusicOverview is the headline summary.
type MusicOverview struct {
	Tracks       int64 `json:"tracks"`
	Artists      int64 `json:"artists"`
	Albums       int64 `json:"albums"`
	TotalSeconds int64 `json:"total_seconds"`
	TotalPlays   int64 `json:"total_plays"`
	Loved        int64 `json:"loved"`
}

func (s *musicStore) Overview() (*MusicOverview, error) {
	var o MusicOverview
	err := s.db.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT artist), COUNT(DISTINCT album),
		       COALESCE(SUM(duration_sec), 0), COALESCE(SUM(play_count), 0), COALESCE(SUM(loved), 0)
		FROM tracks`).Scan(&o.Tracks, &o.Artists, &o.Albums, &o.TotalSeconds, &o.TotalPlays, &o.Loved)
	if err != nil {
		return nil, err
	}
	return &o, nil
}
