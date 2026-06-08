// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — calls.go
// `calls` command group: read your macOS Call History (phone calls via
// Continuity + FaceTime) directly from CallHistory.storedata. Read-only.
// Caller names are resolved from the contacts cache when available, so run
// `contacts sync` first for the richest output.
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newCallsCmd(f *rootFlags) *cobra.Command {
	calls := &cobra.Command{
		Use:   "calls",
		Short: "Read your Call History (phone + FaceTime)",
		Long: `Read your macOS Call History — phone calls placed through Continuity and
FaceTime audio/video — directly from CallHistory.storedata. Read-only.

Requires Full Disk Access (run "icloud-pp-cli doctor" if denied). Caller
names are resolved from the local contacts cache, so run "contacts sync"
first to see names instead of bare numbers.`,
	}

	calls.AddCommand(newCallsListCmd(f))
	calls.AddCommand(newCallsSearchCmd(f))
	calls.AddCommand(newCallsAnalyticsCmd(f))

	return calls
}

func newCallsListCmd(f *rootFlags) *cobra.Command {
	var filter CallFilter
	var incoming, outgoing bool
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent calls (most recent first)",
		Example: `  icloud-pp-cli calls list
  icloud-pp-cli calls list --missed
  icloud-pp-cli calls list --outgoing --limit 50
  icloud-pp-cli calls list --since 2026-06-01`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if incoming && outgoing {
				return usageErr(fmt.Errorf("--incoming and --outgoing are mutually exclusive"))
			}
			if incoming {
				filter.Direction = "incoming"
			}
			if outgoing {
				filter.Direction = "outgoing"
			}
			if sinceStr != "" {
				t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
				if err != nil {
					return usageErr(fmt.Errorf("--since: expected YYYY-MM-DD, got %q", sinceStr))
				}
				filter.Since = t
			}

			db, err := openCallHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			names := newNameResolver()
			calls, err := queryCalls(db, filter, names)
			if err != nil {
				return err
			}
			if len(calls) == 0 {
				fmt.Fprintln(out, "No matching calls.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, calls)
			}
			printCallsTable(f, out, calls)
			fmt.Fprintf(out, "\n%s calls\n", formatInt(int64(len(calls))))
			return nil
		},
	}
	cmd.Flags().BoolVar(&filter.MissedOnly, "missed", false, "Only missed (incoming, unanswered) calls")
	cmd.Flags().BoolVar(&incoming, "incoming", false, "Only incoming calls")
	cmd.Flags().BoolVar(&outgoing, "outgoing", false, "Only outgoing calls")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Only calls on or after this date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&filter.Limit, "limit", 100, "Maximum calls to list")
	return cmd
}

func newCallsSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <number-or-name>",
		Short: "Search calls by phone number, handle, or name",
		Args:  cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli calls search 305
  icloud-pp-cli calls search "Mom"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openCallHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			names := newNameResolver()
			query := joinArgs(args)
			calls, err := searchCalls(db, query, names, limit)
			if err != nil {
				return err
			}
			if len(calls) == 0 {
				fmt.Fprintf(out, "No calls match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, calls)
			}
			printCallsTable(f, out, calls)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(calls))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum results")
	return cmd
}

func newCallsAnalyticsCmd(f *rootFlags) *cobra.Command {
	var top int
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Call overview + most-contacted people",
		Example: `  icloud-pp-cli calls analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openCallHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			names := newNameResolver()
			ov, err := callsOverview(db)
			if err != nil {
				return err
			}
			contacts, err := topCallContacts(db, names, top)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview    *CallsOverview    `json:"overview"`
					TopContacts []CallContactStat `json:"top_contacts"`
				}{ov, contacts})
			}
			printCallsAnalytics(f, out, ov, contacts)
			return nil
		},
	}
	cmd.Flags().IntVar(&top, "top", 15, "Number of top contacts to show")
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printCallsTable(f *rootFlags, out io.Writer, calls []Call) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "When")+"\t"+bold(f, out, "Dir")+"\t"+bold(f, out, "Who")+"\t"+bold(f, out, "Duration")+"\t"+bold(f, out, "Service"))
	for _, c := range calls {
		dir := "→"
		if c.Direction == "incoming" {
			dir = "←"
			if c.Missed {
				dir = red(f, out, "✗")
			}
		}
		who := c.Name
		if who == "" {
			who = c.Address
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			shortDateTime(c.Date), dir, truncate(who, 28), formatDuration(c.DurationSec), c.Service)
	}
	tw.Flush()
}

func printCallsAnalytics(f *rootFlags, out io.Writer, o *CallsOverview, contacts []CallContactStat) {
	fmt.Fprintln(out, bold(f, out, "Call history overview"))
	fmt.Fprintf(out, "  %s total · %s incoming · %s outgoing · %s missed\n",
		formatInt(o.Total), formatInt(o.Incoming), formatInt(o.Outgoing), formatInt(o.Missed))
	fmt.Fprintf(out, "  %s phone · %s FaceTime · %s total talk time\n",
		formatInt(o.Phone), formatInt(o.FaceTime), formatDuration(o.TotalSeconds))
	fmt.Fprintln(out)

	if len(contacts) > 0 {
		fmt.Fprintln(out, bold(f, out, "Most-contacted"))
		tw := newTabWriter(out)
		fmt.Fprintln(tw, bold(f, out, "Who")+"\t"+bold(f, out, "Calls")+"\t"+bold(f, out, "Talk time"))
		for _, c := range contacts {
			who := c.Name
			if who == "" {
				who = c.Address
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\n", truncate(who, 30), c.Calls, formatDuration(c.DurationSec))
		}
		tw.Flush()
	}
}
