// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — calls_db.go
// Read-only access to the macOS Call History database (phone calls placed via
// Continuity + FaceTime audio/video), which lives at
// ~/Library/Application Support/CallHistoryDB/CallHistory.storedata. Like chat.db
// it is a system SQLite database and requires Full Disk Access. There is no sync
// step — queries run directly. Caller names are resolved best-effort from the
// contacts cache (contacts.db) when it exists.
package cli

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func defaultCallHistoryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "CallHistoryDB", "CallHistory.storedata")
}

// openCallHistory opens CallHistory.storedata read-only, surfacing TCC denials
// as a Full Disk Access error (mirrors openMessagesDB / openSafariHistory).
func openCallHistory(path string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf("icloud-pp-cli calls requires macOS — this is %s", runtime.GOOS))
	}
	if path == "" {
		path = defaultCallHistoryPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Call History database not found at %s", path)
		}
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, err
	}
	u := &url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "mode=ro&_busy_timeout=5000&_query_only=1",
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open Call History: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, fmt.Errorf("cannot read Call History: %w", err)
	}
	return db, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// Call is one row from ZCALLRECORD, enriched with a resolved contact name.
type Call struct {
	Address     string `json:"address"`        // phone number or FaceTime handle
	Name        string `json:"name,omitempty"` // resolved from contacts or ZNAME
	Direction   string `json:"direction"`      // "incoming" | "outgoing"
	Answered    bool   `json:"answered"`
	Missed      bool   `json:"missed"`            // incoming and not answered
	Service     string `json:"service,omitempty"` // "phone" | "facetime-audio" | "facetime-video" | raw
	DurationSec int64  `json:"duration_sec"`
	Date        string `json:"date,omitempty"`
}

// CallFilter narrows a call-history query.
type CallFilter struct {
	Direction  string // "" | "incoming" | "outgoing"
	MissedOnly bool
	Since      time.Time // zero = no lower bound
	Limit      int
}

// ── queries ───────────────────────────────────────────────────────────────────

func queryCalls(db *sql.DB, filter CallFilter, names *nameResolver) ([]Call, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	conds := []string{"1 = 1"}
	var args []any
	switch filter.Direction {
	case "incoming":
		conds = append(conds, "ZORIGINATED = 0")
	case "outgoing":
		conds = append(conds, "ZORIGINATED = 1")
	}
	if filter.MissedOnly {
		conds = append(conds, "ZORIGINATED = 0 AND ZANSWERED = 0")
	}
	if !filter.Since.IsZero() {
		conds = append(conds, "ZDATE >= ?")
		args = append(args, float64(filter.Since.Unix()-coreDataEpoch))
	}
	q := fmt.Sprintf(`
		SELECT
			COALESCE(ZADDRESS, ''),
			COALESCE(ZNAME, ''),
			COALESCE(ZORIGINATED, 0),
			COALESCE(ZANSWERED, 0),
			COALESCE(ZSERVICE_PROVIDER, ''),
			COALESCE(ZDURATION, 0),
			COALESCE(ZDATE, 0)
		FROM ZCALLRECORD
		WHERE %s
		ORDER BY ZDATE DESC
		LIMIT %d
	`, joinConds(conds), limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query calls: %w", err)
	}
	defer rows.Close()
	return scanCalls(rows, names)
}

func searchCalls(db *sql.DB, query string, names *nameResolver, limit int) ([]Call, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + query + "%"
	q := fmt.Sprintf(`
		SELECT
			COALESCE(ZADDRESS, ''),
			COALESCE(ZNAME, ''),
			COALESCE(ZORIGINATED, 0),
			COALESCE(ZANSWERED, 0),
			COALESCE(ZSERVICE_PROVIDER, ''),
			COALESCE(ZDURATION, 0),
			COALESCE(ZDATE, 0)
		FROM ZCALLRECORD
		WHERE ZADDRESS LIKE ? OR ZNAME LIKE ?
		ORDER BY ZDATE DESC
		LIMIT %d
	`, limit)
	rows, err := db.Query(q, like, like)
	if err != nil {
		return nil, fmt.Errorf("search calls: %w", err)
	}
	defer rows.Close()
	return scanCalls(rows, names)
}

// scanCalls reads call rows. ZADDRESS is stored as a BLOB in some schema
// versions, so it is scanned as []byte and converted to string.
func scanCalls(rows *sql.Rows, names *nameResolver) ([]Call, error) {
	var out []Call
	for rows.Next() {
		var addr, zname []byte
		var originated, answered int64
		var service string
		var duration, date float64
		if err := rows.Scan(&addr, &zname, &originated, &answered, &service, &duration, &date); err != nil {
			return nil, err
		}
		c := Call{
			Address:     string(addr),
			DurationSec: int64(duration),
			Answered:    answered == 1,
		}
		if originated == 1 {
			c.Direction = "outgoing"
		} else {
			c.Direction = "incoming"
			c.Missed = answered == 0
		}
		c.Service = callServiceLabel(service)
		if date > 0 {
			c.Date = time.Unix(int64(date)+coreDataEpoch, 0).UTC().Format(time.RFC3339)
		}
		// Name resolution: prefer the contacts cache, fall back to ZNAME.
		if names != nil {
			if n := names.lookup(c.Address); n != "" {
				c.Name = n
			}
		}
		if c.Name == "" {
			c.Name = string(zname)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── analytics ───────────────────────────────────────────────────────────────

// CallContactStat ranks an address/contact by call volume.
type CallContactStat struct {
	Address     string `json:"address"`
	Name        string `json:"name,omitempty"`
	Calls       int64  `json:"calls"`
	DurationSec int64  `json:"duration_sec"`
}

// CallsOverview is the headline summary.
type CallsOverview struct {
	Total        int64 `json:"total"`
	Incoming     int64 `json:"incoming"`
	Outgoing     int64 `json:"outgoing"`
	Missed       int64 `json:"missed"`
	TotalSeconds int64 `json:"total_talk_seconds"`
	FaceTime     int64 `json:"facetime_calls"`
	Phone        int64 `json:"phone_calls"`
}

func callsOverview(db *sql.DB) (*CallsOverview, error) {
	var o CallsOverview
	err := db.QueryRow(`
		SELECT
			COUNT(*),
			SUM(CASE WHEN ZORIGINATED = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ZORIGINATED = 1 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ZORIGINATED = 0 AND ZANSWERED = 0 THEN 1 ELSE 0 END),
			COALESCE(SUM(ZDURATION), 0),
			SUM(CASE WHEN ZSERVICE_PROVIDER LIKE '%FaceTime%' THEN 1 ELSE 0 END),
			SUM(CASE WHEN ZSERVICE_PROVIDER NOT LIKE '%FaceTime%' THEN 1 ELSE 0 END)
		FROM ZCALLRECORD
	`).Scan(&o.Total, &o.Incoming, &o.Outgoing, &o.Missed, &o.TotalSeconds, &o.FaceTime, &o.Phone)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func topCallContacts(db *sql.DB, names *nameResolver, limit int) ([]CallContactStat, error) {
	if limit <= 0 {
		limit = 15
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(ZADDRESS, ''), COUNT(*) AS calls, COALESCE(SUM(ZDURATION), 0) AS dur
		FROM ZCALLRECORD
		WHERE ZADDRESS IS NOT NULL AND ZADDRESS != ''
		GROUP BY ZADDRESS
		ORDER BY calls DESC
		LIMIT %d
	`, limit)
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("top call contacts: %w", err)
	}
	defer rows.Close()
	var out []CallContactStat
	for rows.Next() {
		var addr []byte
		var calls int64
		var dur float64
		if err := rows.Scan(&addr, &calls, &dur); err != nil {
			return nil, err
		}
		cs := CallContactStat{Address: string(addr), Calls: calls, DurationSec: int64(dur)}
		if names != nil {
			cs.Name = names.lookup(cs.Address)
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

// ── name resolution ───────────────────────────────────────────────────────────

// nameResolver maps phone numbers and emails to contact display names using the
// local contacts cache (contacts.db). It is best-effort: if the cache is absent
// or unreadable, every lookup returns "" and callers fall back to ZNAME.
type nameResolver struct {
	byPhone map[string]string
	byEmail map[string]string
}

// newNameResolver loads the contacts cache if it exists. It never errors — a nil
// or empty resolver simply yields no matches.
func newNameResolver() *nameResolver {
	r := &nameResolver{byPhone: map[string]string{}, byEmail: map[string]string{}}
	if _, err := os.Stat(contactsDBPath()); err != nil {
		return r // no cache yet — that's fine
	}
	store, err := openContactStore()
	if err != nil {
		return r
	}
	defer store.Close()

	// Phones → name.
	if rows, err := store.db.Query(`
		SELECT cp.normalized, c.first_name, c.last_name
		FROM contact_phones cp JOIN contacts c ON c.id = cp.contact_id
		WHERE cp.normalized != ''`); err == nil {
		for rows.Next() {
			var norm, fn, ln sql.NullString
			if rows.Scan(&norm, &fn, &ln) == nil {
				name := joinName(fn.String, ln.String)
				if norm.String != "" && name != "" {
					r.byPhone[norm.String] = name
				}
			}
		}
		rows.Close()
	}
	// Emails → name.
	if rows, err := store.db.Query(`
		SELECT LOWER(ce.value), c.first_name, c.last_name
		FROM contact_emails ce JOIN contacts c ON c.id = ce.contact_id
		WHERE ce.value != ''`); err == nil {
		for rows.Next() {
			var email, fn, ln sql.NullString
			if rows.Scan(&email, &fn, &ln) == nil {
				name := joinName(fn.String, ln.String)
				if email.String != "" && name != "" {
					r.byEmail[email.String] = name
				}
			}
		}
		rows.Close()
	}
	return r
}

// lookup resolves an address (phone or email) to a contact name, or "".
func (r *nameResolver) lookup(address string) string {
	if r == nil || address == "" {
		return ""
	}
	if strings.Contains(address, "@") {
		return r.byEmail[strings.ToLower(address)]
	}
	if n, ok := r.byPhone[normalizePhone(address)]; ok {
		return n
	}
	return ""
}

func joinName(first, last string) string {
	switch {
	case first != "" && last != "":
		return first + " " + last
	case first != "":
		return first
	default:
		return last
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// callServiceLabel maps the raw ZSERVICE_PROVIDER string to a friendly label.
func callServiceLabel(raw string) string {
	switch {
	case raw == "":
		return ""
	case containsFold(raw, "FaceTimeVideo"):
		return "facetime-video"
	case containsFold(raw, "FaceTimeAudio"):
		return "facetime-audio"
	case containsFold(raw, "FaceTime"):
		return "facetime"
	case containsFold(raw, "Telephony") || containsFold(raw, "PhoneNumber"):
		return "phone"
	default:
		return raw
	}
}

// formatDuration renders seconds as compact "1h 2m", "5m 30s", or "12s".
func formatDuration(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
