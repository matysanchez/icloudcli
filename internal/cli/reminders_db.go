// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — reminders_db.go
// Local SQLite store for Reminders data synced from Reminders.app via JXA.
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

type reminderStore struct {
	db *sql.DB
}

func remindersDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-pp-cli", "reminders.db")
}

func openReminderStore() (*reminderStore, error) {
	path := remindersDBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating reminders db dir: %w", err)
	}
	db, err := sql.Open("sqlite",
		path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON&_temp_store=MEMORY",
	)
	if err != nil {
		return nil, fmt.Errorf("opening reminders db: %w", err)
	}
	db.SetMaxOpenConns(4)
	s := &reminderStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("reminders db migrate: %w", err)
	}
	return s, nil
}

func (s *reminderStore) Close() error { return s.db.Close() }

func (s *reminderStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS reminders (
			id              TEXT PRIMARY KEY,
			uuid            TEXT,
			name            TEXT,
			body            TEXT,
			list_name       TEXT,
			completed       INTEGER DEFAULT 0,
			priority        INTEGER DEFAULT 0,
			flagged         INTEGER DEFAULT 0,
			due_date        TEXT,
			remind_date     TEXT,
			completion_date TEXT,
			created_at      TEXT,
			modified_at     TEXT,
			synced_at       DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_list      ON reminders(list_name)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_completed ON reminders(completed)`,
		`CREATE INDEX IF NOT EXISTS idx_reminders_due       ON reminders(due_date)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_reminders_uuid ON reminders(uuid)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS reminders_fts USING fts5(
			id UNINDEXED,
			body,
			tokenize='unicode61'
		)`,

		`CREATE TABLE IF NOT EXISTS reminders_sync_state (
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

// jxaReminder is the JSON shape emitted by the JXA sync script.
type jxaReminder struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Body           string `json:"body"`
	List           string `json:"list"`
	Completed      bool   `json:"completed"`
	Priority       int    `json:"priority"`
	Flagged        bool   `json:"flagged"`
	DueDate        string `json:"dueDate"`
	RemindDate     string `json:"remindDate"`
	CompletionDate string `json:"completionDate"`
	CreatedAt      string `json:"createdAt"`
	ModifiedAt     string `json:"modifiedAt"`
}

func (s *reminderStore) SyncAll(items []jxaReminder) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM reminders"); err != nil {
		return 0, fmt.Errorf("sync: clear reminders: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM reminders_fts"); err != nil {
		return 0, fmt.Errorf("sync: clear fts: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, r := range items {
		if _, err := tx.Exec(
			`INSERT INTO reminders (id, uuid, name, body, list_name, completed, priority, flagged, due_date, remind_date, completion_date, created_at, modified_at, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, extractNoteUUID(r.ID), r.Name, r.Body, r.List,
			boolToInt(r.Completed), r.Priority, boolToInt(r.Flagged),
			r.DueDate, r.RemindDate, r.CompletionDate, r.CreatedAt, r.ModifiedAt, now,
		); err != nil {
			return 0, fmt.Errorf("sync: insert reminder: %w", err)
		}
		ftsBody := strings.Join([]string{r.Name, r.Body, r.List}, " ")
		if _, err := tx.Exec(`INSERT INTO reminders_fts (id, body) VALUES (?, ?)`, r.ID, ftsBody); err != nil {
			return 0, fmt.Errorf("sync: insert fts: %w", err)
		}
		count++
	}

	if _, err := tx.Exec(
		`INSERT INTO reminders_sync_state (key, value, updated_at) VALUES ('last_sync', ?, CURRENT_TIMESTAMP)
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

// Reminder is a row from the reminders table.
type Reminder struct {
	ID             string `json:"id"`
	UUID           string `json:"uuid,omitempty"`
	Name           string `json:"name"`
	Body           string `json:"body,omitempty"`
	List           string `json:"list,omitempty"`
	Completed      bool   `json:"completed"`
	Priority       int    `json:"priority"`
	PriorityLabel  string `json:"priority_label,omitempty"`
	Flagged        bool   `json:"flagged"`
	DueDate        string `json:"due_date,omitempty"`
	RemindDate     string `json:"remind_date,omitempty"`
	CompletionDate string `json:"completion_date,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	ModifiedAt     string `json:"modified_at,omitempty"`
}

// ReminderFilter narrows a List query.
type ReminderFilter struct {
	List      string // restrict to a named list
	Completed *bool  // nil = both, true = completed only, false = open only
	Overdue   bool   // due before now and not completed
	Upcoming  int    // due within the next N days and not completed (0 = off)
	Limit     int
}

func (s *reminderStore) Count() (int, error) {
	var n int
	return n, s.db.QueryRow("SELECT COUNT(*) FROM reminders").Scan(&n)
}

func (s *reminderStore) LastSyncedAt() string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM reminders_sync_state WHERE key = 'last_sync'").Scan(&v)
	return v
}

func (s *reminderStore) List(filter ReminderFilter) ([]Reminder, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	var conds []string
	var args []any
	if filter.List != "" {
		conds = append(conds, "LOWER(list_name) = LOWER(?)")
		args = append(args, filter.List)
	}
	if filter.Completed != nil {
		conds = append(conds, "completed = ?")
		args = append(args, boolToInt(*filter.Completed))
	}
	nowISO := time.Now().Format(time.RFC3339)
	if filter.Overdue {
		conds = append(conds, "completed = 0 AND due_date != '' AND due_date < ?")
		args = append(args, nowISO)
	}
	if filter.Upcoming > 0 {
		until := time.Now().AddDate(0, 0, filter.Upcoming).Format(time.RFC3339)
		conds = append(conds, "completed = 0 AND due_date != '' AND due_date >= ? AND due_date <= ?")
		args = append(args, nowISO, until)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	// Open reminders sort by due date ascending (soonest first), nulls last.
	q := fmt.Sprintf(
		`SELECT id, uuid, name, body, list_name, completed, priority, flagged, due_date, remind_date, completion_date, created_at, modified_at
		 FROM reminders %s
		 ORDER BY completed ASC,
		          CASE WHEN due_date = '' THEN 1 ELSE 0 END ASC,
		          due_date ASC
		 LIMIT %d`, where, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminderRows(rows)
}

func (s *reminderStore) Get(id string) (*Reminder, error) {
	row := s.db.QueryRow(
		`SELECT id, uuid, name, body, list_name, completed, priority, flagged, due_date, remind_date, completion_date, created_at, modified_at
		 FROM reminders WHERE id = ?`, id,
	)
	r, err := scanReminderRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *reminderStore) GetByAny(input string) (*Reminder, error) {
	if strings.Contains(input, "://") {
		return s.Get(input)
	}
	var id string
	if err := s.db.QueryRow("SELECT id FROM reminders WHERE uuid = ?", input).Scan(&id); err == nil {
		return s.Get(id)
	}
	rows, err := s.db.Query(`SELECT id FROM reminders WHERE uuid LIKE ? || '%' ESCAPE '\' LIMIT 3`, escapeLike(input))
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
		return nil, fmt.Errorf("ambiguous prefix %q matches %d reminders — use more characters", input, len(ids))
	}
}

func (s *reminderStore) Search(query string, limit int) ([]Reminder, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT r.id, r.uuid, r.name, r.body, r.list_name, r.completed, r.priority, r.flagged, r.due_date, r.remind_date, r.completion_date, r.created_at, r.modified_at
		 FROM reminders_fts f JOIN reminders r ON r.id = f.id
		 WHERE reminders_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminderRows(rows)
}

func scanReminderRows(rows *sql.Rows) ([]Reminder, error) {
	var out []Reminder
	for rows.Next() {
		r, err := scanOneReminder(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanReminderRow(row *sql.Row) (*Reminder, error) {
	return scanOneReminder(row.Scan)
}

// scanOneReminder scans the canonical 13-column reminder row using the provided
// Scan function (from either *sql.Rows or *sql.Row).
func scanOneReminder(scan func(...any) error) (*Reminder, error) {
	var r Reminder
	var uuid, name, body, list, due, remind, completion, created, modified sql.NullString
	var completed, priority, flagged sql.NullInt64
	if err := scan(&r.ID, &uuid, &name, &body, &list, &completed, &priority, &flagged,
		&due, &remind, &completion, &created, &modified); err != nil {
		return nil, err
	}
	r.UUID, r.Name, r.Body, r.List = uuid.String, name.String, body.String, list.String
	r.DueDate, r.RemindDate, r.CompletionDate = due.String, remind.String, completion.String
	r.CreatedAt, r.ModifiedAt = created.String, modified.String
	r.Completed, r.Flagged = completed.Int64 == 1, flagged.Int64 == 1
	r.Priority = int(priority.Int64)
	r.PriorityLabel = priorityLabel(r.Priority)
	return &r, nil
}

// ── analytics ───────────────────────────────────────────────────────────────

// ListCount is one row in the per-list breakdown.
type ListCount struct {
	List      string `json:"list"`
	Total     int64  `json:"total"`
	Open      int64  `json:"open"`
	Completed int64  `json:"completed"`
	Overdue   int64  `json:"overdue"`
}

func (s *reminderStore) AnalyticsLists() ([]ListCount, error) {
	nowISO := time.Now().Format(time.RFC3339)
	rows, err := s.db.Query(
		`SELECT COALESCE(NULLIF(list_name, ''), '(none)'),
		        COUNT(*),
		        SUM(CASE WHEN completed = 0 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 1 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND due_date != '' AND due_date < ? THEN 1 ELSE 0 END)
		 FROM reminders GROUP BY list_name ORDER BY COUNT(*) DESC`, nowISO,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ListCount
	for rows.Next() {
		var lc ListCount
		if err := rows.Scan(&lc.List, &lc.Total, &lc.Open, &lc.Completed, &lc.Overdue); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}

// RemindersOverview is the headline summary.
type RemindersOverview struct {
	Total        int64 `json:"total"`
	Open         int64 `json:"open"`
	Completed    int64 `json:"completed"`
	Overdue      int64 `json:"overdue"`
	DueToday     int64 `json:"due_today"`
	DueThisWeek  int64 `json:"due_this_week"`
	NoDueDate    int64 `json:"no_due_date"`
	HighPriority int64 `json:"high_priority_open"`
	Flagged      int64 `json:"flagged_open"`
}

func (s *reminderStore) Overview() (*RemindersOverview, error) {
	var o RemindersOverview
	now := time.Now()
	nowISO := now.Format(time.RFC3339)
	endToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()).Format(time.RFC3339)
	endWeek := now.AddDate(0, 0, 7).Format(time.RFC3339)
	err := s.db.QueryRow(
		`SELECT COUNT(*),
		        SUM(CASE WHEN completed = 0 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 1 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND due_date != '' AND due_date < ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND due_date != '' AND due_date >= ? AND due_date <= ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND due_date != '' AND due_date >= ? AND due_date <= ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND due_date = '' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND priority > 0 AND priority <= 1 THEN 1 ELSE 0 END),
		        SUM(CASE WHEN completed = 0 AND flagged = 1 THEN 1 ELSE 0 END)
		 FROM reminders`,
		nowISO, nowISO, endToday, nowISO, endWeek,
	).Scan(&o.Total, &o.Open, &o.Completed, &o.Overdue, &o.DueToday, &o.DueThisWeek, &o.NoDueDate, &o.HighPriority, &o.Flagged)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// priorityLabel maps Apple's reminder priority integers to human labels. Apple
// uses 0 = none, 1 = high, 5 = medium, 9 = low (with 2-4 / 6-8 as variants).
func priorityLabel(p int) string {
	switch {
	case p == 0:
		return ""
	case p <= 4:
		return "high"
	case p == 5:
		return "medium"
	default:
		return "low"
	}
}
