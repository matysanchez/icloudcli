// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — calendar_db.go
// Local SQLite store for Calendar events synced from Calendar.app via JXA.
// Sync is windowed by date range (events are unbounded; a window keeps the JXA
// query fast and the cache relevant).
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

type calendarStore struct {
	db *sql.DB
}

func calendarDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-pp-cli", "calendar.db")
}

func openCalendarStore() (*calendarStore, error) {
	path := calendarDBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating calendar db dir: %w", err)
	}
	db, err := sql.Open("sqlite",
		path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON&_temp_store=MEMORY",
	)
	if err != nil {
		return nil, fmt.Errorf("opening calendar db: %w", err)
	}
	db.SetMaxOpenConns(4)
	s := &calendarStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("calendar db migrate: %w", err)
	}
	return s, nil
}

func (s *calendarStore) Close() error { return s.db.Close() }

func (s *calendarStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id           TEXT PRIMARY KEY,
			uid          TEXT,
			title        TEXT,
			calendar     TEXT,
			location     TEXT,
			notes        TEXT,
			all_day      INTEGER DEFAULT 0,
			start_date   TEXT,
			end_date     TEXT,
			duration_min INTEGER DEFAULT 0,
			status       TEXT,
			synced_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_start    ON events(start_date)`,
		`CREATE INDEX IF NOT EXISTS idx_events_calendar ON events(calendar)`,

		`CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
			id UNINDEXED,
			body,
			tokenize='unicode61'
		)`,

		`CREATE TABLE IF NOT EXISTS calendar_sync_state (
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

// jxaEvent is the JSON shape emitted by the JXA sync script.
type jxaEvent struct {
	ID        string `json:"id"`
	UID       string `json:"uid"`
	Title     string `json:"title"`
	Calendar  string `json:"calendar"`
	Location  string `json:"location"`
	Notes     string `json:"notes"`
	AllDay    bool   `json:"allDay"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Status    string `json:"status"`
}

// SyncAll replaces all events with the provided window export.
func (s *calendarStore) SyncAll(events []jxaEvent, fromISO, toISO string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM events"); err != nil {
		return 0, fmt.Errorf("sync: clear events: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM events_fts"); err != nil {
		return 0, fmt.Errorf("sync: clear fts: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	count := 0
	for _, e := range events {
		dur := durationMinutes(e.StartDate, e.EndDate)
		if _, err := tx.Exec(
			`INSERT INTO events (id, uid, title, calendar, location, notes, all_day, start_date, end_date, duration_min, status, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.UID, e.Title, e.Calendar, e.Location, e.Notes,
			boolToInt(e.AllDay), e.StartDate, e.EndDate, dur, e.Status, now,
		); err != nil {
			return 0, fmt.Errorf("sync: insert event: %w", err)
		}
		ftsBody := strings.Join([]string{e.Title, e.Location, e.Notes, e.Calendar}, " ")
		if _, err := tx.Exec(`INSERT INTO events_fts (id, body) VALUES (?, ?)`, e.ID, ftsBody); err != nil {
			return 0, fmt.Errorf("sync: insert fts: %w", err)
		}
		count++
	}

	for k, v := range map[string]string{"last_sync": now, "window_from": fromISO, "window_to": toISO} {
		if _, err := tx.Exec(
			`INSERT INTO calendar_sync_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
			k, v,
		); err != nil {
			return 0, fmt.Errorf("sync: record state: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// Event is a row from the events table.
type Event struct {
	ID          string `json:"id"`
	UID         string `json:"uid,omitempty"`
	Title       string `json:"title"`
	Calendar    string `json:"calendar,omitempty"`
	Location    string `json:"location,omitempty"`
	Notes       string `json:"notes,omitempty"`
	AllDay      bool   `json:"all_day"`
	StartDate   string `json:"start_date,omitempty"`
	EndDate     string `json:"end_date,omitempty"`
	DurationMin int    `json:"duration_min"`
	Status      string `json:"status,omitempty"`
}

func (s *calendarStore) Count() (int, error) {
	var n int
	return n, s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
}

func (s *calendarStore) LastSyncedAt() string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM calendar_sync_state WHERE key = 'last_sync'").Scan(&v)
	return v
}

func (s *calendarStore) Window() (from, to string) {
	_ = s.db.QueryRow("SELECT value FROM calendar_sync_state WHERE key = 'window_from'").Scan(&from)
	_ = s.db.QueryRow("SELECT value FROM calendar_sync_state WHERE key = 'window_to'").Scan(&to)
	return from, to
}

// Range returns events that start within [fromISO, toISO], soonest first.
func (s *calendarStore) Range(fromISO, toISO, calendar string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	conds := []string{"start_date >= ?", "start_date <= ?"}
	args := []any{fromISO, toISO}
	if calendar != "" {
		conds = append(conds, "LOWER(calendar) = LOWER(?)")
		args = append(args, calendar)
	}
	q := fmt.Sprintf(
		`SELECT id, uid, title, calendar, location, notes, all_day, start_date, end_date, duration_min, status
		 FROM events WHERE %s ORDER BY start_date ASC LIMIT %d`,
		strings.Join(conds, " AND "), limit,
	)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventRows(rows)
}

func (s *calendarStore) Search(query string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT e.id, e.uid, e.title, e.calendar, e.location, e.notes, e.all_day, e.start_date, e.end_date, e.duration_min, e.status
		 FROM events_fts f JOIN events e ON e.id = f.id
		 WHERE events_fts MATCH ? ORDER BY e.start_date DESC LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEventRows(rows)
}

func scanEventRows(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		var e Event
		var uid, title, cal, loc, notes, start, end, status sql.NullString
		var allDay, dur sql.NullInt64
		if err := rows.Scan(&e.ID, &uid, &title, &cal, &loc, &notes, &allDay, &start, &end, &dur, &status); err != nil {
			return nil, err
		}
		e.UID, e.Title, e.Calendar = uid.String, title.String, cal.String
		e.Location, e.Notes = loc.String, notes.String
		e.StartDate, e.EndDate, e.Status = start.String, end.String, status.String
		e.AllDay = allDay.Int64 == 1
		e.DurationMin = int(dur.Int64)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── analytics ───────────────────────────────────────────────────────────────

// CalendarCount is one row in the per-calendar breakdown.
type CalendarCount struct {
	Calendar string  `json:"calendar"`
	Count    int64   `json:"count"`
	Hours    float64 `json:"hours"`
}

func (s *calendarStore) AnalyticsCalendars() ([]CalendarCount, error) {
	rows, err := s.db.Query(
		`SELECT COALESCE(NULLIF(calendar, ''), '(none)'), COUNT(*), COALESCE(SUM(duration_min), 0) / 60.0
		 FROM events GROUP BY calendar ORDER BY COUNT(*) DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarCount
	for rows.Next() {
		var c CalendarCount
		if err := rows.Scan(&c.Calendar, &c.Count, &c.Hours); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// WeekdayCount is one row in the busiest-weekday breakdown.
type WeekdayCount struct {
	Weekday string `json:"weekday"`
	Count   int64  `json:"count"`
}

// AnalyticsWeekdays computes event counts by day of week in process (SQLite's
// strftime %w gives 0=Sunday..6=Saturday; we map to names and keep week order).
func (s *calendarStore) AnalyticsWeekdays() ([]WeekdayCount, error) {
	rows, err := s.db.Query(`SELECT start_date FROM events WHERE start_date != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make([]int64, 7) // index 0 = Sunday
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		counts[int(t.Weekday())]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	out := make([]WeekdayCount, 0, 7)
	for i, name := range names {
		out = append(out, WeekdayCount{Weekday: name, Count: counts[i]})
	}
	return out, nil
}

// CalendarOverview is the headline summary.
type CalendarOverview struct {
	TotalEvents int64   `json:"total_events"`
	AllDay      int64   `json:"all_day_events"`
	TotalHours  float64 `json:"total_scheduled_hours"`
	Upcoming7   int64   `json:"upcoming_7_days"`
	Today       int64   `json:"today"`
	Calendars   int64   `json:"distinct_calendars"`
}

func (s *calendarStore) Overview() (*CalendarOverview, error) {
	var o CalendarOverview
	now := time.Now()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Format(time.RFC3339)
	endToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()).Format(time.RFC3339)
	nowISO := now.Format(time.RFC3339)
	end7 := now.AddDate(0, 0, 7).Format(time.RFC3339)
	err := s.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(all_day), 0),
		        COALESCE(SUM(duration_min), 0) / 60.0,
		        SUM(CASE WHEN start_date >= ? AND start_date <= ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN start_date >= ? AND start_date <= ? THEN 1 ELSE 0 END),
		        COUNT(DISTINCT calendar)
		 FROM events`,
		nowISO, end7, startToday, endToday,
	).Scan(&o.TotalEvents, &o.AllDay, &o.TotalHours, &o.Upcoming7, &o.Today, &o.Calendars)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// durationMinutes returns the whole-minute gap between two RFC3339 timestamps,
// or 0 if either is unparseable or the range is inverted.
func durationMinutes(startISO, endISO string) int {
	start, err1 := time.Parse(time.RFC3339, startISO)
	end, err2 := time.Parse(time.RFC3339, endISO)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := end.Sub(start).Minutes()
	if d < 0 {
		return 0
	}
	return int(d)
}
