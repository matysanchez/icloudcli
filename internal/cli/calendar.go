// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — calendar.go
// `calendar` command group: read your Apple Calendar (iCloud-synced) from the
// local Calendar.app via JXA into a SQLite cache, then show an agenda, list a
// date range, search, and analyze. Read-only — never creates or edits events.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newCalendarCmd(f *rootFlags) *cobra.Command {
	calendar := &cobra.Command{
		Use:     "calendar",
		Aliases: []string{"cal"},
		Short:   "Read and analyze your Apple Calendar",
		Long: `Read your Apple Calendar (iCloud-synced) via Calendar.app.

'calendar sync' pulls events within a date window (default: 90 days back to
365 days forward) into a local SQLite cache via JavaScript for Automation
(JXA). agenda, list, search, and analytics then run instantly. Read-only.

Requires Automation permission for your terminal to control Calendar.app
(macOS prompts on first sync).`,
	}

	calendar.AddCommand(newCalendarSyncCmd(f))
	calendar.AddCommand(newCalendarAgendaCmd(f))
	calendar.AddCommand(newCalendarListCmd(f))
	calendar.AddCommand(newCalendarSearchCmd(f))
	calendar.AddCommand(newCalendarAnalyticsCmd(f))

	return calendar
}

// jxaCalendarSyncScript exports events within [from, to] across all calendars.
// It uses JXA's whose() date predicate so the Calendar engine does the range
// filtering rather than us iterating every event ever created.
const jxaCalendarSyncScript = `
function run(argv) {
  var fromMs = parseInt(argv[0], 10);
  var toMs = parseInt(argv[1], 10);
  var from = new Date(fromMs);
  var to = new Date(toMs);
  var app = Application("Calendar");
  var out = [];
  function iso(d) { try { return d ? d.toISOString() : ""; } catch (e) { return ""; } }
  function prop(fn) { try { return fn(); } catch (e) { return null; } }
  var cals;
  try { cals = app.calendars(); } catch (e) { cals = []; }
  for (var ci = 0; ci < cals.length; ci++) {
    var cal = cals[ci];
    var calName = "";
    try { calName = cal.name(); } catch (e) {}
    var evs;
    try {
      evs = cal.events.whose({_and: [
        {startDate: {">=": from}},
        {startDate: {"<=": to}}
      ]})();
    } catch (e) {
      try { evs = cal.events(); } catch (e2) { evs = []; }
    }
    for (var ei = 0; ei < evs.length; ei++) {
      var ev = evs[ei];
      out.push({
        id:        prop(function(){ return ev.uid(); }) || (calName + "#" + ei),
        uid:       prop(function(){ return ev.uid(); }) || "",
        title:     prop(function(){ return ev.summary(); }) || "",
        calendar:  calName,
        location:  prop(function(){ return ev.location(); }) || "",
        notes:     prop(function(){ return ev.description(); }) || "",
        allDay:    prop(function(){ return ev.alldayEvent(); }) || false,
        startDate: iso(prop(function(){ return ev.startDate(); })),
        endDate:   iso(prop(function(){ return ev.endDate(); })),
        status:    prop(function(){ return ev.status(); }) || ""
      });
    }
  }
  return JSON.stringify(out);
}
`

func newCalendarSyncCmd(f *rootFlags) *cobra.Command {
	var force bool
	var backDays, fwdDays int
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync calendar events within a date window into the local cache",
		Long: `Pulls events from Calendar.app within a date window via JXA.

The window defaults to 90 days in the past through 365 days in the future.
Widen it with --back and --forward (in days) if you need more history or a
longer horizon — larger windows take longer to sync.`,
		Example: `  icloud-pp-cli calendar sync
  icloud-pp-cli calendar sync --back 365 --forward 365
  icloud-pp-cli calendar sync --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openCalendarStore()
			if err != nil {
				return err
			}
			defer store.Close()

			if !force {
				if last := store.LastSyncedAt(); last != "" {
					from, to := store.Window()
					fmt.Fprintf(out, "  %s Last synced: %s\n", yellow(f, out, "i"), last)
					fmt.Fprintf(out, "  Window: %s → %s\n", shortDate(from), shortDate(to))
					fmt.Fprintln(out, "  Use --force to re-sync.")
					count, _ := store.Count()
					fmt.Fprintf(out, "  %s %s events in local store.\n", green(f, out, "✓"), formatInt(int64(count)))
					return nil
				}
			}

			now := time.Now()
			from := now.AddDate(0, 0, -backDays)
			to := now.AddDate(0, 0, fwdDays)

			fmt.Fprintln(out, bold(f, out, "Syncing calendar events from Calendar.app..."))
			fmt.Fprintf(out, "  → Window: %s → %s\n", from.Format("2006-01-02"), to.Format("2006-01-02"))
			fmt.Fprintln(out, "  → Fetching via JXA (may prompt for Automation access)...")
			start := time.Now()

			fromMs := fmt.Sprintf("%d", from.UnixMilli())
			toMs := fmt.Sprintf("%d", to.UnixMilli())
			raw, err := runJXAForAppArgs("Calendar", jxaCalendarSyncScript, fromMs, toMs)
			if err != nil {
				if isAutomationDenied(err) {
					return configErr(fmt.Errorf("Calendar automation denied.\n%s", automationHint("Calendar")))
				}
				return fmt.Errorf("JXA sync failed: %w", err)
			}

			var events []jxaEvent
			if err := json.Unmarshal([]byte(raw), &events); err != nil {
				return fmt.Errorf("parsing JXA output: %w", err)
			}
			fmt.Fprintf(out, "  → Fetched %s events\n", formatInt(int64(len(events))))

			n, err := store.SyncAll(events, from.Format(time.RFC3339), to.Format(time.RFC3339))
			if err != nil {
				return fmt.Errorf("storing events: %w", err)
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Fprintln(out)
			fmt.Fprintf(out, "%s %s events synced in %s\n", green(f, out, "✓"), formatInt(int64(n)), elapsed)
			fmt.Fprintf(out, "    DB: %s\n", calendarDBPath())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-sync even if already synced")
	cmd.Flags().IntVar(&backDays, "back", 90, "Days of history to include")
	cmd.Flags().IntVar(&fwdDays, "forward", 365, "Days into the future to include")
	return cmd
}

func newCalendarAgendaCmd(f *rootFlags) *cobra.Command {
	var days int
	var calName string
	cmd := &cobra.Command{
		Use:   "agenda",
		Short: "Show upcoming events (next 7 days by default)",
		Example: `  icloud-pp-cli calendar agenda
  icloud-pp-cli calendar agenda --days 30
  icloud-pp-cli calendar agenda --calendar Work`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openCalendarStore()
			if err != nil {
				return err
			}
			defer store.Close()

			now := time.Now()
			from := now.Format(time.RFC3339)
			to := now.AddDate(0, 0, days).Format(time.RFC3339)
			evs, err := store.Range(from, to, calName, 500)
			if err != nil {
				return err
			}
			if len(evs) == 0 {
				fmt.Fprintf(out, "No events in the next %d days. (Run 'calendar sync' if you haven't yet.)\n", days)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, evs)
			}
			printAgenda(f, out, evs)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "Number of days ahead to include")
	cmd.Flags().StringVar(&calName, "calendar", "", "Restrict to a named calendar")
	return cmd
}

func newCalendarListCmd(f *rootFlags) *cobra.Command {
	var fromStr, toStr, calName string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List events in an explicit date range",
		Long:  `List events whose start falls within [--from, --to] (YYYY-MM-DD). Defaults to the full synced window.`,
		Example: `  icloud-pp-cli calendar list --from 2026-01-01 --to 2026-03-31
  icloud-pp-cli calendar list --calendar Personal --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openCalendarStore()
			if err != nil {
				return err
			}
			defer store.Close()

			winFrom, winTo := store.Window()
			fromISO, err := parseDateRangeFlag(fromStr, winFrom)
			if err != nil {
				return usageErr(fmt.Errorf("--from: %w", err))
			}
			toISO, err := parseDateRangeFlag(toStr, winTo)
			if err != nil {
				return usageErr(fmt.Errorf("--to: %w", err))
			}
			evs, err := store.Range(fromISO, toISO, calName, limit)
			if err != nil {
				return err
			}
			if len(evs) == 0 {
				fmt.Fprintln(out, "No events in range. (Run 'calendar sync' if you haven't yet.)")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, evs)
			}
			printEventsTable(f, out, evs)
			fmt.Fprintf(out, "\n%s events\n", formatInt(int64(len(evs))))
			return nil
		},
	}
	cmd.Flags().StringVar(&fromStr, "from", "", "Start of range (YYYY-MM-DD; default: synced window start)")
	cmd.Flags().StringVar(&toStr, "to", "", "End of range (YYYY-MM-DD; default: synced window end)")
	cmd.Flags().StringVar(&calName, "calendar", "", "Restrict to a named calendar")
	cmd.Flags().IntVar(&limit, "limit", 500, "Maximum events to list")
	return cmd
}

func newCalendarSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across event titles, locations, and notes",
		Args:  cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli calendar search dentist
  icloud-pp-cli calendar search "team offsite"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openCalendarStore()
			if err != nil {
				return err
			}
			defer store.Close()

			query := joinArgs(args)
			evs, err := store.Search(query, limit)
			if err != nil {
				return err
			}
			if len(evs) == 0 {
				fmt.Fprintf(out, "No events match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, evs)
			}
			printEventsTable(f, out, evs)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(evs))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	return cmd
}

func newCalendarAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Overview stats, per-calendar and busiest-weekday breakdowns",
		Example: `  icloud-pp-cli calendar analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openCalendarStore()
			if err != nil {
				return err
			}
			defer store.Close()

			ov, err := store.Overview()
			if err != nil {
				return err
			}
			cals, err := store.AnalyticsCalendars()
			if err != nil {
				return err
			}
			weekdays, err := store.AnalyticsWeekdays()
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview  *CalendarOverview `json:"overview"`
					Calendars []CalendarCount   `json:"calendars"`
					Weekdays  []WeekdayCount    `json:"weekdays"`
				}{ov, cals, weekdays})
			}
			printCalendarAnalytics(f, out, ov, cals, weekdays)
			return nil
		},
	}
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

// printAgenda groups events by day with a header per date.
func printAgenda(f *rootFlags, out io.Writer, evs []Event) {
	var lastDay string
	for _, e := range evs {
		day := shortDate(e.StartDate)
		if day != lastDay {
			if lastDay != "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintln(out, bold(f, out, dayHeader(e.StartDate)))
			lastDay = day
		}
		when := eventTimeLabel(e)
		line := fmt.Sprintf("  %s  %s", when, e.Title)
		if e.Location != "" {
			line += "  · " + truncate(e.Location, 30)
		}
		if e.Calendar != "" {
			line += "  " + yellow(f, out, "["+e.Calendar+"]")
		}
		fmt.Fprintln(out, line)
	}
}

func printEventsTable(f *rootFlags, out io.Writer, evs []Event) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "When")+"\t"+bold(f, out, "Event")+"\t"+bold(f, out, "Calendar")+"\t"+bold(f, out, "Location"))
	for _, e := range evs {
		when := shortDate(e.StartDate)
		if !e.AllDay {
			when = shortDateTime(e.StartDate)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			when, truncate(e.Title, 36), truncate(e.Calendar, 16), truncate(e.Location, 24))
	}
	tw.Flush()
}

func printCalendarAnalytics(f *rootFlags, out io.Writer, o *CalendarOverview, cals []CalendarCount, weekdays []WeekdayCount) {
	fmt.Fprintln(out, bold(f, out, "Calendar overview"))
	fmt.Fprintf(out, "  %s events · %s all-day · %.0f scheduled hours · %s calendars\n",
		formatInt(o.TotalEvents), formatInt(o.AllDay), o.TotalHours, formatInt(o.Calendars))
	fmt.Fprintf(out, "  %s today · %s in next 7 days\n", formatInt(o.Today), formatInt(o.Upcoming7))
	fmt.Fprintln(out)

	if len(cals) > 0 {
		fmt.Fprintln(out, bold(f, out, "By calendar"))
		tw := newTabWriter(out)
		fmt.Fprintln(tw, bold(f, out, "Calendar")+"\t"+bold(f, out, "Events")+"\t"+bold(f, out, "Hours"))
		for _, c := range cals {
			fmt.Fprintf(tw, "%s\t%d\t%.0f\n", truncate(c.Calendar, 28), c.Count, c.Hours)
		}
		tw.Flush()
		fmt.Fprintln(out)
	}

	if len(weekdays) > 0 {
		fmt.Fprintln(out, bold(f, out, "Busiest weekday"))
		var maxCount int64 = 1
		for _, w := range weekdays {
			if w.Count > maxCount {
				maxCount = w.Count
			}
		}
		tw := newTabWriter(out)
		for _, w := range weekdays {
			bar := barOfWidth(int(w.Count*20/maxCount), 20)
			fmt.Fprintf(tw, "  %s\t%s\t%d\n", w.Weekday[:3], bar, w.Count)
		}
		tw.Flush()
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// eventTimeLabel renders the time portion of an event for the agenda: "all-day"
// for all-day events, otherwise "HH:MM–HH:MM".
func eventTimeLabel(e Event) string {
	if e.AllDay {
		return "all-day"
	}
	start, err1 := time.Parse(time.RFC3339, e.StartDate)
	if err1 != nil {
		return "     "
	}
	label := start.Local().Format("15:04")
	if end, err2 := time.Parse(time.RFC3339, e.EndDate); err2 == nil {
		label += "–" + end.Local().Format("15:04")
	}
	return label
}

// dayHeader renders a friendly day header like "Mon, Jan 2 2006".
func dayHeader(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return shortDate(iso)
	}
	return t.Local().Format("Mon, Jan 2 2006")
}

// parseDateRangeFlag turns a YYYY-MM-DD flag into an RFC3339 timestamp; an empty flag
// falls back to the provided default ISO string.
func parseDateRangeFlag(flag, fallback string) (string, error) {
	if flag == "" {
		if fallback == "" {
			// No synced window recorded; use a wide default.
			return time.Now().AddDate(-1, 0, 0).Format(time.RFC3339), nil
		}
		return fallback, nil
	}
	t, err := time.ParseInLocation("2006-01-02", flag, time.Local)
	if err != nil {
		return "", fmt.Errorf("expected YYYY-MM-DD, got %q", flag)
	}
	return t.Format(time.RFC3339), nil
}

// barOfWidth returns a unicode bar of n filled blocks padded to width.
func barOfWidth(n, width int) string {
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	bar := make([]rune, width)
	for i := 0; i < width; i++ {
		if i < n {
			bar[i] = '█'
		} else {
			bar[i] = '░'
		}
	}
	return string(bar)
}
