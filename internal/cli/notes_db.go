// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — notes_db.go
// Local SQLite store for Notes data synced from Notes.app via JXA. Mirrors the
// contacts store: typed columns + an FTS5 virtual table for full-text search +
// a sync_state row recording the last sync time.
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

// ── store ─────────────────────────────────────────────────────────────────────

type noteStore struct {
	db *sql.DB
}

func notesDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-pp-cli", "notes.db")
}

func openNoteStore() (*noteStore, error) {
	path := notesDBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating notes db dir: %w", err)
	}
	db, err := sql.Open("sqlite",
		path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON&_temp_store=MEMORY",
	)
	if err != nil {
		return nil, fmt.Errorf("opening notes db: %w", err)
	}
	db.SetMaxOpenConns(4)
	s := &noteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("notes db migrate: %w", err)
	}
	return s, nil
}

func (s *noteStore) Close() error { return s.db.Close() }

func (s *noteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS notes (
			id           TEXT PRIMARY KEY,
			uuid         TEXT,
			name         TEXT,
			body         TEXT,
			snippet      TEXT,
			folder       TEXT,
			account      TEXT,
			char_count   INTEGER,
			word_count   INTEGER,
			shared       INTEGER DEFAULT 0,
			pwd_protected INTEGER DEFAULT 0,
			created_at   TEXT,
			modified_at  TEXT,
			synced_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_folder   ON notes(folder)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_account  ON notes(account)`,
		`CREATE INDEX IF NOT EXISTS idx_notes_modified ON notes(modified_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_notes_uuid ON notes(uuid)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
			id UNINDEXED,
			body,
			tokenize='unicode61'
		)`,

		`CREATE TABLE IF NOT EXISTS notes_sync_state (
			key        TEXT PRIMARY KEY,
			value      TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, stmt[:min(60, len(stmt))])
		}
	}
	return nil
}

// ── sync ──────────────────────────────────────────────────────────────────────

// jxaNote is the JSON shape emitted by the JXA sync script.
type jxaNote struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Body         string `json:"body"`
	Folder       string `json:"folder"`
	Account      string `json:"account"`
	Shared       bool   `json:"shared"`
	PwdProtected bool   `json:"pwdProtected"`
	CreatedAt    string `json:"createdAt"`
	ModifiedAt   string `json:"modifiedAt"`
}

// SyncAll replaces all note rows with the provided JXA-exported list, rebuilding
// the FTS index in the same transaction.
func (s *noteStore) SyncAll(notes []jxaNote) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM notes"); err != nil {
		return 0, fmt.Errorf("sync: clear notes: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM notes_fts"); err != nil {
		return 0, fmt.Errorf("sync: clear fts: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, n := range notes {
		body := strings.TrimSpace(n.Body)
		snippet := makeSnippet(body, 140)
		charCount := len([]rune(body))
		wordCount := len(strings.Fields(body))
		shared := 0
		if n.Shared {
			shared = 1
		}
		pwd := 0
		if n.PwdProtected {
			pwd = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO notes (id, uuid, name, body, snippet, folder, account, char_count, word_count, shared, pwd_protected, created_at, modified_at, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, extractNoteUUID(n.ID), n.Name, body, snippet, n.Folder, n.Account,
			charCount, wordCount, shared, pwd, n.CreatedAt, n.ModifiedAt, now,
		); err != nil {
			return 0, fmt.Errorf("sync: insert note: %w", err)
		}
		ftsBody := strings.Join([]string{n.Name, body}, " ")
		if _, err := tx.Exec(`INSERT INTO notes_fts (id, body) VALUES (?, ?)`, n.ID, ftsBody); err != nil {
			return 0, fmt.Errorf("sync: insert fts: %w", err)
		}
		count++
	}

	if _, err := tx.Exec(
		`INSERT INTO notes_sync_state (key, value, updated_at) VALUES ('last_sync', ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		now,
	); err != nil {
		return 0, fmt.Errorf("sync: record state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// Note is a row from the notes table.
type Note struct {
	ID           string `json:"id"`
	UUID         string `json:"uuid,omitempty"`
	Name         string `json:"name"`
	Body         string `json:"body,omitempty"`
	Snippet      string `json:"snippet,omitempty"`
	Folder       string `json:"folder,omitempty"`
	Account      string `json:"account,omitempty"`
	CharCount    int    `json:"char_count"`
	WordCount    int    `json:"word_count"`
	Shared       bool   `json:"shared"`
	PwdProtected bool   `json:"pwd_protected"`
	CreatedAt    string `json:"created_at,omitempty"`
	ModifiedAt   string `json:"modified_at,omitempty"`
}

func (s *noteStore) Count() (int, error) {
	var n int
	return n, s.db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&n)
}

func (s *noteStore) LastSyncedAt() string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM notes_sync_state WHERE key = 'last_sync'").Scan(&v)
	return v
}

// List returns notes ordered by most-recently modified.
func (s *noteStore) List(limit, offset int) ([]Note, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, uuid, name, snippet, folder, account, char_count, word_count, shared, pwd_protected, created_at, modified_at
		 FROM notes ORDER BY modified_at DESC LIMIT ? OFFSET ?`, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNoteRows(rows)
}

// Get resolves a note by full Notes ID. Returns (nil, nil) when not found.
func (s *noteStore) Get(id string) (*Note, error) {
	row := s.db.QueryRow(
		`SELECT id, uuid, name, body, snippet, folder, account, char_count, word_count, shared, pwd_protected, created_at, modified_at
		 FROM notes WHERE id = ?`, id,
	)
	return scanNoteDetail(row)
}

// GetByAny resolves a note by full ID, exact UUID, or UUID prefix. Errors on an
// ambiguous prefix; returns (nil, nil) when nothing matches.
func (s *noteStore) GetByAny(input string) (*Note, error) {
	if strings.Contains(input, "://") {
		return s.Get(input)
	}
	var id string
	err := s.db.QueryRow("SELECT id FROM notes WHERE uuid = ?", input).Scan(&id)
	if err == nil {
		return s.Get(id)
	}
	rows, err := s.db.Query(`SELECT id FROM notes WHERE uuid LIKE ? || '%' ESCAPE '\' LIMIT 3`, escapeLike(input))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var rid string
		if err := rows.Scan(&rid); err != nil {
			return nil, err
		}
		ids = append(ids, rid)
	}
	rows.Close()
	switch len(ids) {
	case 0:
		return nil, nil
	case 1:
		return s.Get(ids[0])
	default:
		return nil, fmt.Errorf("ambiguous prefix %q matches %d notes — use more characters", input, len(ids))
	}
}

// Search runs an FTS5 MATCH over note titles and bodies.
func (s *noteStore) Search(query string, limit int) ([]Note, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT n.id, n.uuid, n.name, n.snippet, n.folder, n.account, n.char_count, n.word_count, n.shared, n.pwd_protected, n.created_at, n.modified_at
		 FROM notes_fts f JOIN notes n ON n.id = f.id
		 WHERE notes_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNoteRows(rows)
}

func scanNoteRows(rows *sql.Rows) ([]Note, error) {
	var out []Note
	for rows.Next() {
		var n Note
		var uuid, name, snippet, folder, account, created, modified sql.NullString
		var shared, pwd sql.NullInt64
		if err := rows.Scan(&n.ID, &uuid, &name, &snippet, &folder, &account,
			&n.CharCount, &n.WordCount, &shared, &pwd, &created, &modified); err != nil {
			return nil, err
		}
		n.UUID, n.Name, n.Snippet = uuid.String, name.String, snippet.String
		n.Folder, n.Account = folder.String, account.String
		n.CreatedAt, n.ModifiedAt = created.String, modified.String
		n.Shared, n.PwdProtected = shared.Int64 == 1, pwd.Int64 == 1
		out = append(out, n)
	}
	return out, rows.Err()
}

func scanNoteDetail(row *sql.Row) (*Note, error) {
	var n Note
	var uuid, name, body, snippet, folder, account, created, modified sql.NullString
	var shared, pwd sql.NullInt64
	err := row.Scan(&n.ID, &uuid, &name, &body, &snippet, &folder, &account,
		&n.CharCount, &n.WordCount, &shared, &pwd, &created, &modified)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.UUID, n.Name, n.Body, n.Snippet = uuid.String, name.String, body.String, snippet.String
	n.Folder, n.Account = folder.String, account.String
	n.CreatedAt, n.ModifiedAt = created.String, modified.String
	n.Shared, n.PwdProtected = shared.Int64 == 1, pwd.Int64 == 1
	return &n, nil
}

// ── analytics ───────────────────────────────────────────────────────────────

// FolderCount is one row in the by-folder breakdown.
type FolderCount struct {
	Folder  string `json:"folder"`
	Account string `json:"account,omitempty"`
	Count   int64  `json:"count"`
	Words   int64  `json:"words"`
}

func (s *noteStore) AnalyticsFolders() ([]FolderCount, error) {
	rows, err := s.db.Query(
		`SELECT COALESCE(NULLIF(folder, ''), '(none)'), COALESCE(account, ''), COUNT(*), COALESCE(SUM(word_count), 0)
		 FROM notes GROUP BY folder, account ORDER BY COUNT(*) DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderCount
	for rows.Next() {
		var fc FolderCount
		if err := rows.Scan(&fc.Folder, &fc.Account, &fc.Count, &fc.Words); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	return out, rows.Err()
}

// NotesOverview is the headline analytics summary.
type NotesOverview struct {
	TotalNotes   int64   `json:"total_notes"`
	TotalWords   int64   `json:"total_words"`
	TotalChars   int64   `json:"total_chars"`
	AvgWords     float64 `json:"avg_words_per_note"`
	SharedNotes  int64   `json:"shared_notes"`
	LockedNotes  int64   `json:"locked_notes"`
	EmptyNotes   int64   `json:"empty_notes"`
	LongestWords int64   `json:"longest_note_words"`
	LongestName  string  `json:"longest_note_name,omitempty"`
}

func (s *noteStore) Overview() (*NotesOverview, error) {
	var o NotesOverview
	err := s.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(word_count), 0),
		        COALESCE(SUM(char_count), 0),
		        COALESCE(SUM(shared), 0),
		        COALESCE(SUM(pwd_protected), 0),
		        COALESCE(SUM(CASE WHEN word_count = 0 THEN 1 ELSE 0 END), 0)
		 FROM notes`,
	).Scan(&o.TotalNotes, &o.TotalWords, &o.TotalChars, &o.SharedNotes, &o.LockedNotes, &o.EmptyNotes)
	if err != nil {
		return nil, err
	}
	if o.TotalNotes > 0 {
		o.AvgWords = float64(o.TotalWords) / float64(o.TotalNotes)
	}
	var name sql.NullString
	var words sql.NullInt64
	_ = s.db.QueryRow(`SELECT name, word_count FROM notes ORDER BY word_count DESC LIMIT 1`).Scan(&name, &words)
	o.LongestName, o.LongestWords = name.String, words.Int64
	return &o, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// extractNoteUUID pulls the trailing identifier from a Notes Core Data URL like
// "x-coredata://<store-uuid>/ICNote/p123" so users can reference a note by a
// short token instead of the full URL. Falls back to the full id.
func extractNoteUUID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}

// makeSnippet returns the first maxRunes runes of a body with newlines collapsed
// to spaces, suffixed with an ellipsis when truncated.
func makeSnippet(body string, maxRunes int) string {
	flat := strings.Join(strings.Fields(body), " ")
	r := []rune(flat)
	if len(r) <= maxRunes {
		return flat
	}
	return string(r[:maxRunes]) + "…"
}
