// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — safari_db.go
// Read-only access to Safari's local data: History.db (a SQLite database) and
// Bookmarks.plist. History.db lives under ~/Library/Safari and, like chat.db,
// requires Full Disk Access. Unlike the JXA command groups there is no sync
// step — queries run directly against Safari's own database in read-only mode.
package cli

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

// safariEpoch is the Cocoa/CFAbsoluteTime reference date offset: Safari stores
// visit_time as seconds since 2001-01-01 UTC. Add this to convert to Unix.
const safariEpoch int64 = 978307200

func defaultSafariHistoryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Safari", "History.db")
}

func defaultSafariBookmarksPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Safari", "Bookmarks.plist")
}

// openSafariHistory opens History.db read-only. Mirrors openMessagesDB: the
// file: URI with mode=ro is load-bearing on modernc.org/sqlite, and TCC denials
// are surfaced as a Full Disk Access error rather than a confusing driver error.
func openSafariHistory(path string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf("icloud-pp-cli safari requires macOS — this is %s", runtime.GOOS))
	}
	if path == "" {
		path = defaultSafariHistoryPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Safari History.db not found at %s", path)
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
		return nil, fmt.Errorf("cannot open History.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, fmt.Errorf("cannot read History.db: %w", err)
	}
	return db, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// HistoryVisit is one visited URL with its most recent visit time and title.
type HistoryVisit struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Domain     string `json:"domain,omitempty"`
	VisitCount int    `json:"visit_count"`
	LastVisit  string `json:"last_visit,omitempty"`
}

// DomainVisit aggregates visits by domain for the top-sites view.
type DomainVisit struct {
	Domain     string `json:"domain"`
	VisitCount int64  `json:"visit_count"`
	URLs       int64  `json:"distinct_urls"`
}

// HistoryFilter narrows a history query.
type HistoryFilter struct {
	Domain string
	Since  time.Time // zero = no lower bound
	Limit  int
}

// ── queries ───────────────────────────────────────────────────────────────────

// History returns recently-visited URLs, most recent first. Aggregates the
// history_visits rows per history_item so each URL appears once with its visit
// count and latest visit time.
func queryHistory(db *sql.DB, filter HistoryFilter) ([]HistoryVisit, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	conds := []string{"1 = 1"}
	var args []any
	if filter.Domain != "" {
		conds = append(conds, "i.domain_expansion LIKE ? OR i.url LIKE ?")
		args = append(args, "%"+filter.Domain+"%", "%"+filter.Domain+"%")
	}
	if !filter.Since.IsZero() {
		conds = append(conds, "v.visit_time >= ?")
		args = append(args, float64(filter.Since.Unix()-safariEpoch))
	}
	q := fmt.Sprintf(`
		SELECT i.url,
		       COALESCE((SELECT vv.title FROM history_visits vv WHERE vv.history_item = i.id AND vv.title IS NOT NULL ORDER BY vv.visit_time DESC LIMIT 1), ''),
		       COALESCE(i.domain_expansion, ''),
		       i.visit_count,
		       MAX(v.visit_time)
		FROM history_items i
		JOIN history_visits v ON v.history_item = i.id
		WHERE %s
		GROUP BY i.id
		ORDER BY MAX(v.visit_time) DESC
		LIMIT %d
	`, joinConds(conds), limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	return scanVisits(rows)
}

// searchHistory matches the query against URL and title.
func searchHistory(db *sql.DB, query string, limit int) ([]HistoryVisit, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + query + "%"
	q := fmt.Sprintf(`
		SELECT i.url,
		       COALESCE((SELECT vv.title FROM history_visits vv WHERE vv.history_item = i.id AND vv.title IS NOT NULL ORDER BY vv.visit_time DESC LIMIT 1), ''),
		       COALESCE(i.domain_expansion, ''),
		       i.visit_count,
		       MAX(v.visit_time)
		FROM history_items i
		JOIN history_visits v ON v.history_item = i.id
		WHERE i.url LIKE ? OR EXISTS (
			SELECT 1 FROM history_visits t WHERE t.history_item = i.id AND t.title LIKE ?
		)
		GROUP BY i.id
		ORDER BY MAX(v.visit_time) DESC
		LIMIT %d
	`, limit)
	rows, err := db.Query(q, like, like)
	if err != nil {
		return nil, fmt.Errorf("search history: %w", err)
	}
	defer rows.Close()
	return scanVisits(rows)
}

// topDomains aggregates total visits by domain.
func topDomains(db *sql.DB, since time.Time, limit int) ([]DomainVisit, error) {
	if limit <= 0 {
		limit = 25
	}
	conds := []string{"i.domain_expansion IS NOT NULL", "i.domain_expansion != ''"}
	var args []any
	if !since.IsZero() {
		conds = append(conds, "v.visit_time >= ?")
		args = append(args, float64(since.Unix()-safariEpoch))
	}
	q := fmt.Sprintf(`
		SELECT i.domain_expansion, COUNT(v.id) AS visits, COUNT(DISTINCT i.id) AS urls
		FROM history_items i
		JOIN history_visits v ON v.history_item = i.id
		WHERE %s
		GROUP BY i.domain_expansion
		ORDER BY visits DESC
		LIMIT %d
	`, joinConds(conds), limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("top domains: %w", err)
	}
	defer rows.Close()
	var out []DomainVisit
	for rows.Next() {
		var d DomainVisit
		if err := rows.Scan(&d.Domain, &d.VisitCount, &d.URLs); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanVisits(rows *sql.Rows) ([]HistoryVisit, error) {
	var out []HistoryVisit
	for rows.Next() {
		var v HistoryVisit
		var title, domain sql.NullString
		var visitTime sql.NullFloat64
		if err := rows.Scan(&v.URL, &title, &domain, &v.VisitCount, &visitTime); err != nil {
			return nil, err
		}
		v.Title, v.Domain = title.String, domain.String
		if visitTime.Valid {
			t := time.Unix(int64(visitTime.Float64)+safariEpoch, 0).UTC()
			v.LastVisit = t.Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── history overview ──────────────────────────────────────────────────────────

// SafariOverview is the headline history summary.
type SafariOverview struct {
	TotalURLs    int64  `json:"total_urls"`
	TotalVisits  int64  `json:"total_visits"`
	DistinctDoms int64  `json:"distinct_domains"`
	OldestVisit  string `json:"oldest_visit,omitempty"`
	NewestVisit  string `json:"newest_visit,omitempty"`
}

func safariOverview(db *sql.DB) (*SafariOverview, error) {
	var o SafariOverview
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(visit_count), 0), COUNT(DISTINCT domain_expansion) FROM history_items`).
		Scan(&o.TotalURLs, &o.TotalVisits, &o.DistinctDoms); err != nil {
		return nil, err
	}
	var oldest, newest sql.NullFloat64
	_ = db.QueryRow(`SELECT MIN(visit_time), MAX(visit_time) FROM history_visits`).Scan(&oldest, &newest)
	if oldest.Valid {
		o.OldestVisit = time.Unix(int64(oldest.Float64)+safariEpoch, 0).UTC().Format(time.RFC3339)
	}
	if newest.Valid {
		o.NewestVisit = time.Unix(int64(newest.Float64)+safariEpoch, 0).UTC().Format(time.RFC3339)
	}
	return &o, nil
}

func joinConds(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += "(" + c + ")"
	}
	return out
}
