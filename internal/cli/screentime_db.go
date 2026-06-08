// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — screentime_db.go
// Read-only access to the macOS Screen Time / Knowledge store, knowledgeC.db.
// It's a SQLite event log where each row in ZOBJECT is a typed event on a named
// stream (e.g. "/app/usage", "/app/webUsage", "/notification/usage"). We query
// the streams that map to the Screen Time surfaces people care about. FDA-gated.
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

func defaultKnowledgeDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Knowledge", "knowledgeC.db")
}

// openKnowledgeDB opens knowledgeC.db read-only, surfacing TCC denials as a Full
// Disk Access error (same pattern as messages/safari/calls).
func openKnowledgeDB(path string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf("icloud-pp-cli screentime requires macOS — this is %s", runtime.GOOS))
	}
	if path == "" {
		path = defaultKnowledgeDBPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Screen Time database not found at %s", path)
		}
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_busy_timeout=5000&_query_only=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open knowledgeC.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, fmt.Errorf("cannot read knowledgeC.db: %w", err)
	}
	return db, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// UsageStat is one app/domain with an aggregated metric (seconds or a count).
type UsageStat struct {
	Key     string `json:"key"`   // bundle id or domain
	Label   string `json:"label"` // friendlier leaf name
	Seconds int64  `json:"seconds,omitempty"`
	Count   int64  `json:"count,omitempty"`
}

// ── queries ───────────────────────────────────────────────────────────────────

// appUsage aggregates foreground app usage seconds from the /app/usage stream,
// since the given cutoff. ZSTARTDATE/ZENDDATE are Cocoa-epoch seconds.
func appUsage(db *sql.DB, since time.Time, limit int) ([]UsageStat, error) {
	return streamDurations(db, "/app/usage", since, limit)
}

// webUsage aggregates Safari web-domain usage seconds from /app/webUsage.
func webUsage(db *sql.DB, since time.Time, limit int) ([]UsageStat, error) {
	return streamDurations(db, "/app/webUsage", since, limit)
}

func streamDurations(db *sql.DB, stream string, since time.Time, limit int) ([]UsageStat, error) {
	if limit <= 0 {
		limit = 25
	}
	args := []any{stream}
	cond := ""
	if !since.IsZero() {
		cond = "AND ZSTARTDATE >= ?"
		args = append(args, float64(since.Unix()-coreDataEpoch))
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(ZVALUESTRING, '(unknown)'),
		       CAST(COALESCE(SUM(ZENDDATE - ZSTARTDATE), 0) AS INTEGER) AS secs
		FROM ZOBJECT
		WHERE ZSTREAMNAME = ? %s
		  AND ZVALUESTRING IS NOT NULL AND ZVALUESTRING != ''
		  AND ZENDDATE > ZSTARTDATE
		GROUP BY ZVALUESTRING
		ORDER BY secs DESC
		LIMIT %d
	`, cond, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("screentime %s: %w", stream, err)
	}
	defer rows.Close()
	var out []UsageStat
	for rows.Next() {
		var s UsageStat
		if err := rows.Scan(&s.Key, &s.Seconds); err != nil {
			return nil, err
		}
		s.Label = appLeaf(s.Key)
		out = append(out, s)
	}
	return out, rows.Err()
}

// notificationCounts aggregates notifications per app from /notification/usage.
func notificationCounts(db *sql.DB, since time.Time, limit int) ([]UsageStat, error) {
	if limit <= 0 {
		limit = 25
	}
	args := []any{"/notification/usage"}
	cond := ""
	if !since.IsZero() {
		cond = "AND ZSTARTDATE >= ?"
		args = append(args, float64(since.Unix()-coreDataEpoch))
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(ZVALUESTRING, '(unknown)'), COUNT(*) AS cnt
		FROM ZOBJECT
		WHERE ZSTREAMNAME = ? %s
		  AND ZVALUESTRING IS NOT NULL AND ZVALUESTRING != ''
		GROUP BY ZVALUESTRING
		ORDER BY cnt DESC
		LIMIT %d
	`, cond, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("screentime notifications: %w", err)
	}
	defer rows.Close()
	var out []UsageStat
	for rows.Next() {
		var s UsageStat
		if err := rows.Scan(&s.Key, &s.Count); err != nil {
			return nil, err
		}
		s.Label = appLeaf(s.Key)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ScreenTimeOverview is the headline summary over the window.
type ScreenTimeOverview struct {
	WindowDays    int    `json:"window_days"`
	TotalSeconds  int64  `json:"total_app_seconds"`
	DistinctApps  int64  `json:"distinct_apps"`
	Notifications int64  `json:"notifications"`
	TopApp        string `json:"top_app,omitempty"`
	TopAppSeconds int64  `json:"top_app_seconds,omitempty"`
}

func screenTimeOverview(db *sql.DB, since time.Time, windowDays int) (*ScreenTimeOverview, error) {
	o := ScreenTimeOverview{WindowDays: windowDays}
	args := []any{"/app/usage"}
	cond := ""
	if !since.IsZero() {
		cond = "AND ZSTARTDATE >= ?"
		args = append(args, float64(since.Unix()-coreDataEpoch))
	}
	err := db.QueryRow(fmt.Sprintf(`
		SELECT CAST(COALESCE(SUM(ZENDDATE - ZSTARTDATE), 0) AS INTEGER),
		       COUNT(DISTINCT ZVALUESTRING)
		FROM ZOBJECT
		WHERE ZSTREAMNAME = ? %s AND ZENDDATE > ZSTARTDATE
	`, cond), args...).Scan(&o.TotalSeconds, &o.DistinctApps)
	if err != nil {
		return nil, err
	}
	// Notification total over the same window.
	nargs := []any{"/notification/usage"}
	ncond := ""
	if !since.IsZero() {
		ncond = "AND ZSTARTDATE >= ?"
		nargs = append(nargs, float64(since.Unix()-coreDataEpoch))
	}
	_ = db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM ZOBJECT WHERE ZSTREAMNAME = ? %s`, ncond), nargs...).Scan(&o.Notifications)
	// Top app.
	if top, err := appUsage(db, since, 1); err == nil && len(top) > 0 {
		o.TopApp = top[0].Label
		o.TopAppSeconds = top[0].Seconds
	}
	return &o, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// appLeaf turns a bundle id into a friendlier label: the last dotted segment,
// with a few well-known IDs mapped to recognizable names. Domains (web usage)
// pass through unchanged.
func appLeaf(id string) string {
	if id == "" {
		return "(unknown)"
	}
	if name, ok := knownBundles[id]; ok {
		return name
	}
	if !strings.Contains(id, ".") || strings.Contains(id, "/") {
		return id // a domain or path-like value
	}
	parts := strings.Split(id, ".")
	return parts[len(parts)-1]
}

// knownBundles maps a few common Apple bundle IDs to recognizable names so the
// most-used apps read cleanly; everything else falls back to the bundle leaf.
var knownBundles = map[string]string{
	"com.apple.mobilesafari": "Safari",
	"com.apple.Safari":       "Safari",
	"com.apple.MobileSMS":    "Messages",
	"com.apple.mobilemail":   "Mail",
	"com.apple.mail":         "Mail",
	"com.apple.mobilephone":  "Phone",
	"com.apple.mobilecal":    "Calendar",
	"com.apple.iCal":         "Calendar",
	"com.apple.mobilenotes":  "Notes",
	"com.apple.Notes":        "Notes",
	"com.apple.reminders":    "Reminders",
	"com.apple.Music":        "Music",
	"com.apple.podcasts":     "Podcasts",
	"com.apple.MobileStore":  "App Store",
	"com.apple.finder":       "Finder",
	"com.apple.Terminal":     "Terminal",
	"com.googlecode.iterm2":  "iTerm",
	"com.microsoft.VSCode":   "VS Code",
}
