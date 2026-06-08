// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — podcasts_db.go
// Read-only access to the Apple Podcasts library (MTLibrary.sqlite) in the
// Podcasts group container. It's the user's own container, so no Full Disk
// Access is required. Read directly — there is no sync step.
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

func defaultPodcastsDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Group Containers",
		"243LU875E5.groups.com.apple.podcasts", "Documents", "MTLibrary.sqlite")
}

func openPodcastsDB(path string) (*sql.DB, error) {
	if runtime.GOOS != "darwin" {
		return nil, configErr(fmt.Errorf("icloud-pp-cli podcasts requires macOS — this is %s", runtime.GOOS))
	}
	if path == "" {
		path = defaultPodcastsDBPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Podcasts library not found at %s (is Apple Podcasts set up?)", path)
		}
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_busy_timeout=5000&_query_only=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open Podcasts library: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		if isPermissionError(err) {
			return nil, configErr(fmt.Errorf("%w: %s", errFDADenied, path))
		}
		return nil, fmt.Errorf("cannot read Podcasts library: %w", err)
	}
	return db, nil
}

// ── types ─────────────────────────────────────────────────────────────────────

// Show is one podcast in the library.
type Show struct {
	Title      string `json:"title"`
	Author     string `json:"author,omitempty"`
	Subscribed bool   `json:"subscribed"`
	Episodes   int64  `json:"episodes"`
}

// Episode is one podcast episode.
type Episode struct {
	Title       string `json:"title"`
	Show        string `json:"show,omitempty"`
	DurationSec int64  `json:"duration_sec"`
	Published   string `json:"published,omitempty"`
	Downloaded  bool   `json:"downloaded"`
	PlayCount   int64  `json:"play_count"`
	LastPlayed  string `json:"last_played,omitempty"`
}

// EpisodeFilter narrows an episode query.
type EpisodeFilter struct {
	Show           string
	DownloadedOnly bool
	UnplayedOnly   bool
	Limit          int
}

// ── queries ───────────────────────────────────────────────────────────────────

func queryShows(db *sql.DB, subscribedOnly bool, limit int) ([]Show, error) {
	if limit <= 0 {
		limit = 100
	}
	cond := ""
	if subscribedOnly {
		cond = "WHERE p.ZSUBSCRIBED = 1"
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(p.ZTITLE, ''),
		       COALESCE(p.ZAUTHOR, ''),
		       COALESCE(p.ZSUBSCRIBED, 0),
		       (SELECT COUNT(*) FROM ZMTEPISODE e WHERE e.ZPODCAST = p.Z_PK)
		FROM ZMTPODCAST p
		%s
		ORDER BY p.ZSUBSCRIBED DESC, p.ZTITLE COLLATE NOCASE
		LIMIT %d
	`, cond, limit)
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query shows: %w", err)
	}
	defer rows.Close()
	var out []Show
	for rows.Next() {
		var s Show
		var sub int64
		if err := rows.Scan(&s.Title, &s.Author, &sub, &s.Episodes); err != nil {
			return nil, err
		}
		s.Subscribed = sub == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func queryEpisodes(db *sql.DB, filter EpisodeFilter) ([]Episode, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	conds := []string{"1 = 1"}
	var args []any
	if filter.Show != "" {
		conds = append(conds, "LOWER(p.ZTITLE) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.Show)+"%")
	}
	if filter.DownloadedOnly {
		conds = append(conds, "e.ZASSETURL IS NOT NULL")
	}
	if filter.UnplayedOnly {
		conds = append(conds, "COALESCE(e.ZPLAYCOUNT, 0) = 0")
	}
	q := fmt.Sprintf(`
		SELECT COALESCE(e.ZTITLE, ''),
		       COALESCE(p.ZTITLE, ''),
		       COALESCE(e.ZDURATION, 0),
		       COALESCE(e.ZPUBDATE, 0),
		       CASE WHEN e.ZASSETURL IS NOT NULL THEN 1 ELSE 0 END,
		       COALESCE(e.ZPLAYCOUNT, 0),
		       COALESCE(e.ZLASTDATEPLAYED, 0)
		FROM ZMTEPISODE e
		LEFT JOIN ZMTPODCAST p ON p.Z_PK = e.ZPODCAST
		WHERE %s
		ORDER BY e.ZPUBDATE DESC
		LIMIT %d
	`, joinConds(conds), limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query episodes: %w", err)
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		var dur, pub, dl, plays, last float64
		if err := rows.Scan(&e.Title, &e.Show, &dur, &pub, &dl, &plays, &last); err != nil {
			return nil, err
		}
		e.DurationSec = int64(dur)
		e.Downloaded = dl == 1
		e.PlayCount = int64(plays)
		if pub > 0 {
			e.Published = time.Unix(int64(pub)+coreDataEpoch, 0).UTC().Format(time.RFC3339)
		}
		if last > 0 {
			e.LastPlayed = time.Unix(int64(last)+coreDataEpoch, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PodcastsOverview is the headline summary.
type PodcastsOverview struct {
	Shows           int64 `json:"shows"`
	Subscribed      int64 `json:"subscribed"`
	Episodes        int64 `json:"episodes"`
	Downloaded      int64 `json:"downloaded"`
	Played          int64 `json:"played"`
	ListenedSeconds int64 `json:"listened_seconds_est"`
}

func podcastsOverview(db *sql.DB) (*PodcastsOverview, error) {
	var o PodcastsOverview
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ZSUBSCRIBED), 0) FROM ZMTPODCAST`).
		Scan(&o.Shows, &o.Subscribed); err != nil {
		return nil, err
	}
	err := db.QueryRow(`
		SELECT COUNT(*),
		       SUM(CASE WHEN ZASSETURL IS NOT NULL THEN 1 ELSE 0 END),
		       SUM(CASE WHEN COALESCE(ZPLAYCOUNT, 0) > 0 THEN 1 ELSE 0 END),
		       CAST(COALESCE(SUM(CASE WHEN COALESCE(ZPLAYCOUNT,0) > 0 THEN ZDURATION ELSE 0 END), 0) AS INTEGER)
		FROM ZMTEPISODE
	`).Scan(&o.Episodes, &o.Downloaded, &o.Played, &o.ListenedSeconds)
	if err != nil {
		return nil, err
	}
	return &o, nil
}
