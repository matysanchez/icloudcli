// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — music.go
// `music` command group: read your Music library (Apple Music / iCloud Music
// Library) from Music.app via JXA into a SQLite cache, then list / search /
// analyze tracks and list playlists. Read-only — never plays or edits.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newMusicCmd(f *rootFlags) *cobra.Command {
	music := &cobra.Command{
		Use:   "music",
		Short: "Read and analyze your Music library",
		Long: `Read your Music library (Apple Music / iCloud Music Library) via Music.app.

A one-time 'music sync' pulls every library track into a local SQLite cache
via JavaScript for Automation (JXA); list, search, analytics, and playlists
then run instantly. All operations are read-only.

Requires Automation permission for your terminal to control Music.app
(macOS prompts on first sync).`,
	}

	music.AddCommand(newMusicSyncCmd(f))
	music.AddCommand(newMusicListCmd(f))
	music.AddCommand(newMusicSearchCmd(f))
	music.AddCommand(newMusicPlaylistsCmd(f))
	music.AddCommand(newMusicAnalyticsCmd(f))

	return music
}

// jxaMusicSyncScript exports every library track via JXA.
const jxaMusicSyncScript = `
function run() {
  var app = Application("Music");
  var out = [];
  function p(fn){ try { return fn(); } catch (e) { return null; } }
  var tracks;
  try { tracks = app.tracks(); } catch (e) { tracks = []; }
  for (var i = 0; i < tracks.length; i++) {
    var t = tracks[i];
    out.push({
      id:          p(function(){ return t.persistentID(); }) || String(i),
      name:        p(function(){ return t.name(); }) || "",
      artist:      p(function(){ return t.artist(); }) || "",
      album:       p(function(){ return t.album(); }) || "",
      albumArtist: p(function(){ return t.albumArtist(); }) || "",
      genre:       p(function(){ return t.genre(); }) || "",
      year:        p(function(){ return t.year(); }) || 0,
      duration:    Math.round(p(function(){ return t.duration(); }) || 0),
      playCount:   p(function(){ return t.playedCount(); }) || 0,
      rating:      p(function(){ return t.rating(); }) || 0,
      loved:       p(function(){ return t.loved(); }) || false,
      dateAdded:   p(function(){ var d = t.dateAdded(); return d ? d.toISOString() : ""; }) || ""
    });
  }
  return JSON.stringify(out);
}
`

// jxaPlaylistsScript lists playlists with their track counts and durations.
const jxaPlaylistsScript = `
function run() {
  var app = Application("Music");
  var out = [];
  function p(fn){ try { return fn(); } catch (e) { return null; } }
  var pls;
  try { pls = app.playlists(); } catch (e) { pls = []; }
  for (var i = 0; i < pls.length; i++) {
    var pl = pls[i];
    out.push({
      name:     p(function(){ return pl.name(); }) || "",
      count:    p(function(){ return pl.tracks().length; }) || 0,
      duration: Math.round(p(function(){ return pl.duration(); }) || 0),
      special:  p(function(){ return pl.specialKind(); }) || "none"
    });
  }
  return JSON.stringify(out);
}
`

func newMusicSyncCmd(f *rootFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "sync",
		Short:   "Sync your Music library into the local cache",
		Example: `  icloud-pp-cli music sync`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openMusicStore()
			if err != nil {
				return err
			}
			defer store.Close()
			if !force {
				if last := store.LastSyncedAt(); last != "" {
					fmt.Fprintf(out, "  %s Last synced: %s\n", yellow(f, out, "i"), last)
					fmt.Fprintln(out, "  Use --force to re-sync.")
					count, _ := store.Count()
					fmt.Fprintf(out, "  %s %s tracks in local store.\n", green(f, out, "✓"), formatInt(int64(count)))
					return nil
				}
			}
			fmt.Fprintln(out, bold(f, out, "Syncing Music library from Music.app..."))
			fmt.Fprintln(out, "  → Fetching via JXA (may prompt for Automation access; large libraries take a moment)...")
			start := time.Now()
			raw, err := runJXAForApp("Music", jxaMusicSyncScript)
			if err != nil {
				if isAutomationDenied(err) {
					return configErr(fmt.Errorf("Music automation denied.\n%s", automationHint("Music")))
				}
				return fmt.Errorf("JXA sync failed: %w", err)
			}
			var tracks []jxaTrack
			if err := json.Unmarshal([]byte(raw), &tracks); err != nil {
				return fmt.Errorf("parsing JXA output: %w", err)
			}
			fmt.Fprintf(out, "  → Fetched %s tracks\n", formatInt(int64(len(tracks))))
			n, err := store.SyncAll(tracks)
			if err != nil {
				return fmt.Errorf("storing tracks: %w", err)
			}
			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Fprintln(out)
			fmt.Fprintf(out, "%s %s tracks synced in %s\n", green(f, out, "✓"), formatInt(int64(n)), elapsed)
			fmt.Fprintf(out, "    DB: %s\n", musicDBPath())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-sync even if already synced")
	return cmd
}

func newMusicListCmd(f *rootFlags) *cobra.Command {
	var sortKey, artist string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tracks (most-played by default)",
		Example: `  icloud-pp-cli music list
  icloud-pp-cli music list --sort recent
  icloud-pp-cli music list --artist "Daft Punk"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if sortKey != "plays" && sortKey != "recent" && sortKey != "name" {
				return usageErr(fmt.Errorf("--sort must be one of: plays, recent, name"))
			}
			store, err := openMusicStore()
			if err != nil {
				return err
			}
			defer store.Close()
			tracks, err := store.List(sortKey, artist, limit)
			if err != nil {
				return err
			}
			if len(tracks) == 0 {
				fmt.Fprintln(out, "No tracks. Run: icloud-pp-cli music sync")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, tracks)
			}
			printTracksTable(f, out, tracks)
			return nil
		},
	}
	cmd.Flags().StringVar(&sortKey, "sort", "plays", "Sort by: plays, recent, name")
	cmd.Flags().StringVar(&artist, "artist", "", "Filter to an artist (substring)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum tracks")
	return cmd
}

func newMusicSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "search <query>",
		Short:   "Full-text search tracks by name, artist, album, or genre",
		Args:    cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli music search "get lucky"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openMusicStore()
			if err != nil {
				return err
			}
			defer store.Close()
			query := joinArgs(args)
			tracks, err := store.Search(query, limit)
			if err != nil {
				return err
			}
			if len(tracks) == 0 {
				fmt.Fprintf(out, "No tracks match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, tracks)
			}
			printTracksTable(f, out, tracks)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(tracks))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	return cmd
}

func newMusicPlaylistsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "playlists",
		Short:   "List playlists with track counts (live via JXA)",
		Example: `  icloud-pp-cli music playlists`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			raw, err := runJXAForApp("Music", jxaPlaylistsScript)
			if err != nil {
				if isAutomationDenied(err) {
					return configErr(fmt.Errorf("Music automation denied.\n%s", automationHint("Music")))
				}
				return fmt.Errorf("JXA failed: %w", err)
			}
			var pls []musicPlaylist
			if err := json.Unmarshal([]byte(raw), &pls); err != nil {
				return fmt.Errorf("parsing JXA output: %w", err)
			}
			if len(pls) == 0 {
				fmt.Fprintln(out, "No playlists found.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, pls)
			}
			tw := newTabWriter(out)
			fmt.Fprintln(tw, bold(f, out, "Playlist")+"\t"+bold(f, out, "Tracks")+"\t"+bold(f, out, "Duration"))
			for _, pl := range pls {
				fmt.Fprintf(tw, "%s\t%d\t%s\n", truncate(pl.Name, 36), pl.Count, formatDuration(int64(pl.Duration)))
			}
			tw.Flush()
			return nil
		},
	}
	return cmd
}

type musicPlaylist struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Duration int    `json:"duration"`
	Special  string `json:"special"`
}

func newMusicAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Library overview + top artists and genres",
		Example: `  icloud-pp-cli music analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openMusicStore()
			if err != nil {
				return err
			}
			defer store.Close()
			ov, err := store.Overview()
			if err != nil {
				return err
			}
			artists, err := store.TopArtists(15)
			if err != nil {
				return err
			}
			genres, err := store.TopGenres(10)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview   *MusicOverview `json:"overview"`
					TopArtists []ArtistStat   `json:"top_artists"`
					TopGenres  []GenreStat    `json:"top_genres"`
				}{ov, artists, genres})
			}
			fmt.Fprintln(out, bold(f, out, "Music library"))
			fmt.Fprintf(out, "  %s tracks · %s artists · %s albums\n",
				formatInt(ov.Tracks), formatInt(ov.Artists), formatInt(ov.Albums))
			fmt.Fprintf(out, "  %s total runtime · %s total plays · %s loved\n",
				formatDuration(ov.TotalSeconds), formatInt(ov.TotalPlays), formatInt(ov.Loved))
			fmt.Fprintln(out)
			if len(artists) > 0 {
				fmt.Fprintln(out, bold(f, out, "Top artists (by plays)"))
				tw := newTabWriter(out)
				fmt.Fprintln(tw, bold(f, out, "Artist")+"\t"+bold(f, out, "Plays")+"\t"+bold(f, out, "Tracks"))
				for _, a := range artists {
					fmt.Fprintf(tw, "%s\t%d\t%d\n", truncate(a.Artist, 30), a.Plays, a.Tracks)
				}
				tw.Flush()
				fmt.Fprintln(out)
			}
			if len(genres) > 0 {
				fmt.Fprintln(out, bold(f, out, "Top genres"))
				tw := newTabWriter(out)
				for _, g := range genres {
					fmt.Fprintf(tw, "  %s\t%d\n", truncate(g.Genre, 24), g.Tracks)
				}
				tw.Flush()
			}
			return nil
		},
	}
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printTracksTable(f *rootFlags, out io.Writer, tracks []Track) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Title")+"\t"+bold(f, out, "Artist")+"\t"+bold(f, out, "Plays")+"\t"+bold(f, out, "Length")+"\t"+bold(f, out, ""))
	for _, t := range tracks {
		love := ""
		if t.Loved {
			love = "♥"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
			truncate(t.Name, 32), truncate(t.Artist, 24), t.PlayCount, formatDuration(int64(t.DurationSec)), love)
	}
	tw.Flush()
}
