// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — podcasts.go
// `podcasts` command group: read your Apple Podcasts library (shows + episodes)
// directly from MTLibrary.sqlite. Read-only, no special permission (it's your
// own container), no sync step.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newPodcastsCmd(f *rootFlags) *cobra.Command {
	pod := &cobra.Command{
		Use:   "podcasts",
		Short: "Read your Apple Podcasts shows and episodes",
		Long: `Read your Apple Podcasts library (subscriptions, episodes, download and
play state) directly from MTLibrary.sqlite. Read-only; no special permission
needed and no sync step.`,
	}
	pod.AddCommand(newPodcastsShowsCmd(f))
	pod.AddCommand(newPodcastsEpisodesCmd(f))
	pod.AddCommand(newPodcastsAnalyticsCmd(f))
	return pod
}

func newPodcastsShowsCmd(f *rootFlags) *cobra.Command {
	var all bool
	var limit int
	cmd := &cobra.Command{
		Use:   "shows",
		Short: "List podcast shows (subscribed first)",
		Example: `  icloud-pp-cli podcasts shows
  icloud-pp-cli podcasts shows --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openPodcastsDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			shows, err := queryShows(db, !all, limit)
			if err != nil {
				return err
			}
			if len(shows) == 0 {
				fmt.Fprintln(out, "No podcasts found.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, shows)
			}
			tw := newTabWriter(out)
			fmt.Fprintln(tw, bold(f, out, "")+"\t"+bold(f, out, "Show")+"\t"+bold(f, out, "Author")+"\t"+bold(f, out, "Episodes"))
			for _, s := range shows {
				sub := " "
				if s.Subscribed {
					sub = green(f, out, "★")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", sub, truncate(s.Title, 36), truncate(s.Author, 24), s.Episodes)
			}
			tw.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include shows you're not subscribed to")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum shows")
	return cmd
}

func newPodcastsEpisodesCmd(f *rootFlags) *cobra.Command {
	var filter EpisodeFilter
	cmd := &cobra.Command{
		Use:   "episodes",
		Short: "List episodes (newest first)",
		Example: `  icloud-pp-cli podcasts episodes
  icloud-pp-cli podcasts episodes --show "Daily" --downloaded
  icloud-pp-cli podcasts episodes --unplayed --limit 30`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openPodcastsDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			eps, err := queryEpisodes(db, filter)
			if err != nil {
				return err
			}
			if len(eps) == 0 {
				fmt.Fprintln(out, "No matching episodes.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, eps)
			}
			printEpisodesTable(f, out, eps)
			fmt.Fprintf(out, "\n%s episodes\n", formatInt(int64(len(eps))))
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.Show, "show", "", "Filter to a show (substring)")
	cmd.Flags().BoolVar(&filter.DownloadedOnly, "downloaded", false, "Only downloaded episodes")
	cmd.Flags().BoolVar(&filter.UnplayedOnly, "unplayed", false, "Only unplayed episodes")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "Maximum episodes")
	return cmd
}

func newPodcastsAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Library overview — shows, episodes, downloads, listening time",
		Example: `  icloud-pp-cli podcasts analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openPodcastsDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			ov, err := podcastsOverview(db)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, ov)
			}
			fmt.Fprintln(out, bold(f, out, "Podcasts library"))
			fmt.Fprintf(out, "  %s shows (%s subscribed) · %s episodes\n",
				formatInt(ov.Shows), formatInt(ov.Subscribed), formatInt(ov.Episodes))
			fmt.Fprintf(out, "  %s downloaded · %s played · ~%s listened\n",
				formatInt(ov.Downloaded), formatInt(ov.Played), formatDuration(ov.ListenedSeconds))
			return nil
		},
	}
	return cmd
}

func printEpisodesTable(f *rootFlags, out io.Writer, eps []Episode) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Published")+"\t"+bold(f, out, "Episode")+"\t"+bold(f, out, "Show")+"\t"+bold(f, out, "Length")+"\t"+bold(f, out, ""))
	for _, e := range eps {
		flags := ""
		if e.Downloaded {
			flags += green(f, out, "⤓")
		}
		if e.PlayCount == 0 {
			flags += " ●"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			shortDate(e.Published), truncate(e.Title, 34), truncate(e.Show, 20), formatDuration(e.DurationSec), flags)
	}
	tw.Flush()
}
