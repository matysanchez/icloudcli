// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — screentime.go
// `screentime` command group: app/website usage and notification counts read
// from the macOS Knowledge store (knowledgeC.db). Read-only. Knowledge data is
// retained for roughly four weeks, so windows are short by design.
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newScreenTimeCmd(f *rootFlags) *cobra.Command {
	st := &cobra.Command{
		Use:     "screentime",
		Aliases: []string{"st"},
		Short:   "App usage, web usage, and notifications from Screen Time",
		Long: `Read Screen Time data from the macOS Knowledge store (knowledgeC.db).

Shows foreground app usage, Safari web-domain usage, and notification counts.
The Knowledge store typically retains only the last ~4 weeks, so the default
window is 7 days (widen with --days or --since).

Requires Full Disk Access (run "icloud-pp-cli doctor" if denied).`,
	}

	st.AddCommand(newScreenTimeUsageCmd(f))
	st.AddCommand(newScreenTimeWebCmd(f))
	st.AddCommand(newScreenTimeNotificationsCmd(f))
	st.AddCommand(newScreenTimeAnalyticsCmd(f))

	return st
}

// resolveSince converts --days / --since flags into a cutoff time. --since wins
// when both are set; otherwise it's now minus days.
func resolveSince(days int, sinceStr string) (time.Time, int, error) {
	if sinceStr != "" {
		t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
		if err != nil {
			return time.Time{}, 0, fmt.Errorf("--since: expected YYYY-MM-DD, got %q", sinceStr)
		}
		return t, int(time.Since(t).Hours()/24) + 1, nil
	}
	if days <= 0 {
		days = 7
	}
	return time.Now().AddDate(0, 0, -days), days, nil
}

func newScreenTimeUsageCmd(f *rootFlags) *cobra.Command {
	var days, limit int
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Top apps by foreground time",
		Example: `  icloud-pp-cli screentime usage
  icloud-pp-cli screentime usage --days 1
  icloud-pp-cli screentime usage --since 2026-06-01 --limit 30`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			since, _, err := resolveSince(days, sinceStr)
			if err != nil {
				return usageErr(err)
			}
			db, err := openKnowledgeDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			stats, err := appUsage(db, since, limit)
			if err != nil {
				return err
			}
			return renderUsage(f, out, stats, "No app usage recorded in this window.", true)
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Start date (YYYY-MM-DD); overrides --days")
	cmd.Flags().IntVar(&limit, "limit", 25, "Maximum apps")
	return cmd
}

func newScreenTimeWebCmd(f *rootFlags) *cobra.Command {
	var days, limit int
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Top web domains by time (Safari)",
		Example: `  icloud-pp-cli screentime web
  icloud-pp-cli screentime web --days 30`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			since, _, err := resolveSince(days, sinceStr)
			if err != nil {
				return usageErr(err)
			}
			db, err := openKnowledgeDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			stats, err := webUsage(db, since, limit)
			if err != nil {
				return err
			}
			return renderUsage(f, out, stats, "No web usage recorded in this window.", true)
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Start date (YYYY-MM-DD); overrides --days")
	cmd.Flags().IntVar(&limit, "limit", 25, "Maximum domains")
	return cmd
}

func newScreenTimeNotificationsCmd(f *rootFlags) *cobra.Command {
	var days, limit int
	var sinceStr string
	cmd := &cobra.Command{
		Use:     "notifications",
		Aliases: []string{"notifs"},
		Short:   "Notification counts per app",
		Example: `  icloud-pp-cli screentime notifications
  icloud-pp-cli screentime notifications --days 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			since, _, err := resolveSince(days, sinceStr)
			if err != nil {
				return usageErr(err)
			}
			db, err := openKnowledgeDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			stats, err := notificationCounts(db, since, limit)
			if err != nil {
				return err
			}
			return renderUsage(f, out, stats, "No notifications recorded in this window.", false)
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Start date (YYYY-MM-DD); overrides --days")
	cmd.Flags().IntVar(&limit, "limit", 25, "Maximum apps")
	return cmd
}

func newScreenTimeAnalyticsCmd(f *rootFlags) *cobra.Command {
	var days int
	var sinceStr string
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Screen Time overview for the window",
		Example: `  icloud-pp-cli screentime analytics --days 7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			since, windowDays, err := resolveSince(days, sinceStr)
			if err != nil {
				return usageErr(err)
			}
			db, err := openKnowledgeDB("")
			if err != nil {
				return err
			}
			defer db.Close()
			ov, err := screenTimeOverview(db, since, windowDays)
			if err != nil {
				return err
			}
			topApps, err := appUsage(db, since, 5)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview *ScreenTimeOverview `json:"overview"`
					TopApps  []UsageStat         `json:"top_apps"`
				}{ov, topApps})
			}
			fmt.Fprintln(out, bold(f, out, "Screen Time overview"))
			fmt.Fprintf(out, "  window: last %d days\n", ov.WindowDays)
			fmt.Fprintf(out, "  %s total app time · %s apps · %s notifications\n",
				formatDuration(ov.TotalSeconds), formatInt(ov.DistinctApps), formatInt(ov.Notifications))
			if ov.TopApp != "" {
				fmt.Fprintf(out, "  most used: %s (%s)\n", ov.TopApp, formatDuration(ov.TopAppSeconds))
			}
			fmt.Fprintln(out)
			if len(topApps) > 0 {
				fmt.Fprintln(out, bold(f, out, "Top apps"))
				renderUsageTable(f, out, topApps, true)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "Window size in days")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Start date (YYYY-MM-DD); overrides --days")
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

// renderUsage prints a usage list as JSON or a table. durationMode selects the
// seconds column (true) vs a count column (false).
func renderUsage(f *rootFlags, out io.Writer, stats []UsageStat, emptyMsg string, durationMode bool) error {
	if len(stats) == 0 {
		fmt.Fprintln(out, emptyMsg)
		return nil
	}
	if f.asJSON || !isTerminal(out) {
		return printJSON(out, stats)
	}
	renderUsageTable(f, out, stats, durationMode)
	return nil
}

func renderUsageTable(f *rootFlags, out io.Writer, stats []UsageStat, durationMode bool) {
	tw := newTabWriter(out)
	metric := "Time"
	if !durationMode {
		metric = "Count"
	}
	fmt.Fprintln(tw, bold(f, out, "App / Domain")+"\t"+bold(f, out, metric))
	for _, s := range stats {
		if durationMode {
			fmt.Fprintf(tw, "%s\t%s\n", truncate(s.Label, 36), formatDuration(s.Seconds))
		} else {
			fmt.Fprintf(tw, "%s\t%d\n", truncate(s.Label, 36), s.Count)
		}
	}
	tw.Flush()
}
