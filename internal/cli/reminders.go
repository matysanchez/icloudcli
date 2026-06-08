// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — reminders.go
// `reminders` command group: read your Apple Reminders (iCloud-synced) from the
// local Reminders.app via JXA into a SQLite cache, then list / get / search /
// analyze them. Read-only — never creates, completes, or deletes reminders.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newRemindersCmd(f *rootFlags) *cobra.Command {
	reminders := &cobra.Command{
		Use:   "reminders",
		Short: "Read and analyze your Apple Reminders",
		Long: `Read your Apple Reminders (iCloud-synced) via Reminders.app.

A one-time 'reminders sync' pulls every reminder into a local SQLite cache
via JavaScript for Automation (JXA); list, get, search, and analytics then
run instantly. All operations are read-only.

Requires Automation permission for your terminal to control Reminders.app
(macOS prompts on first sync).`,
	}

	reminders.AddCommand(newRemindersSyncCmd(f))
	reminders.AddCommand(newRemindersListCmd(f))
	reminders.AddCommand(newRemindersGetCmd(f))
	reminders.AddCommand(newRemindersSearchCmd(f))
	reminders.AddCommand(newRemindersAnalyticsCmd(f))

	return reminders
}

// jxaRemindersSyncScript exports every reminder via JXA, walking lists so each
// reminder carries its list name. Each property is read defensively because not
// every reminder has every field populated.
const jxaRemindersSyncScript = `
function run() {
  var app = Application("Reminders");
  var out = [];
  function iso(d) { try { return d ? d.toISOString() : ""; } catch (e) { return ""; } }
  function prop(fn) { try { return fn(); } catch (e) { return null; } }
  var lists;
  try { lists = app.lists(); } catch (e) { lists = []; }
  for (var li = 0; li < lists.length; li++) {
    var list = lists[li];
    var listName = "";
    try { listName = list.name(); } catch (e) {}
    var items;
    try { items = list.reminders(); } catch (e) { items = []; }
    for (var ri = 0; ri < items.length; ri++) {
      var r = items[ri];
      out.push({
        id:             prop(function(){ return r.id(); }) || "",
        name:           prop(function(){ return r.name(); }) || "",
        body:           prop(function(){ return r.body(); }) || "",
        list:           listName,
        completed:      prop(function(){ return r.completed(); }) || false,
        priority:       prop(function(){ return r.priority(); }) || 0,
        flagged:        prop(function(){ return r.flagged(); }) || false,
        dueDate:        iso(prop(function(){ return r.dueDate(); })),
        remindDate:     iso(prop(function(){ return r.remindMeDate(); })),
        completionDate: iso(prop(function(){ return r.completionDate(); })),
        createdAt:      iso(prop(function(){ return r.creationDate(); })),
        modifiedAt:     iso(prop(function(){ return r.modificationDate(); }))
      });
    }
  }
  return JSON.stringify(out);
}
`

func newRemindersSyncCmd(f *rootFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync reminders from Reminders.app into the local cache",
		Example: `  icloud-pp-cli reminders sync
  icloud-pp-cli reminders sync --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openReminderStore()
			if err != nil {
				return err
			}
			defer store.Close()

			if !force {
				if last := store.LastSyncedAt(); last != "" {
					fmt.Fprintf(out, "  %s Last synced: %s\n", yellow(f, out, "i"), last)
					fmt.Fprintln(out, "  Use --force to re-sync.")
					count, _ := store.Count()
					fmt.Fprintf(out, "  %s %s reminders in local store.\n", green(f, out, "✓"), formatInt(int64(count)))
					return nil
				}
			}

			fmt.Fprintln(out, bold(f, out, "Syncing reminders from Reminders.app..."))
			fmt.Fprintln(out, "  → Fetching via JXA (may prompt for Automation access)...")
			start := time.Now()

			raw, err := runJXAForApp("Reminders", jxaRemindersSyncScript)
			if err != nil {
				if isAutomationDenied(err) {
					return configErr(fmt.Errorf("Reminders automation denied.\n%s", automationHint("Reminders")))
				}
				return fmt.Errorf("JXA sync failed: %w", err)
			}

			var items []jxaReminder
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				return fmt.Errorf("parsing JXA output: %w", err)
			}
			fmt.Fprintf(out, "  → Fetched %s reminders\n", formatInt(int64(len(items))))

			n, err := store.SyncAll(items)
			if err != nil {
				return fmt.Errorf("storing reminders: %w", err)
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Fprintln(out)
			fmt.Fprintf(out, "%s %s reminders synced in %s\n", green(f, out, "✓"), formatInt(int64(n)), elapsed)
			fmt.Fprintf(out, "    DB: %s\n", remindersDBPath())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-sync even if already synced")
	return cmd
}

func newRemindersListCmd(f *rootFlags) *cobra.Command {
	var filter ReminderFilter
	var showCompleted, showAll bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List reminders (open by default, soonest due first)",
		Long: `List reminders from the local cache. By default shows only open
(incomplete) reminders. Use --completed for completed ones, --all for both,
--overdue for past-due open reminders, and --upcoming N for reminders due
within the next N days.`,
		Example: `  icloud-pp-cli reminders list
  icloud-pp-cli reminders list --list "Groceries"
  icloud-pp-cli reminders list --overdue
  icloud-pp-cli reminders list --upcoming 7
  icloud-pp-cli reminders list --completed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openReminderStore()
			if err != nil {
				return err
			}
			defer store.Close()

			// Resolve the completed filter: default open-only unless --all,
			// --completed, --overdue, or --upcoming changes the intent.
			switch {
			case showAll:
				filter.Completed = nil
			case showCompleted:
				t := true
				filter.Completed = &t
			case filter.Overdue || filter.Upcoming > 0:
				// overdue/upcoming already imply open in the store query
			default:
				fl := false
				filter.Completed = &fl
			}

			rs, err := store.List(filter)
			if err != nil {
				return err
			}
			if len(rs) == 0 {
				fmt.Fprintln(out, "No matching reminders. (Run 'reminders sync' if you haven't yet.)")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, rs)
			}
			printRemindersTable(f, out, rs)
			fmt.Fprintf(out, "\n%s reminders\n", formatInt(int64(len(rs))))
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.List, "list", "", "Restrict to a named list")
	cmd.Flags().BoolVar(&showCompleted, "completed", false, "Show completed reminders instead of open ones")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show both open and completed")
	cmd.Flags().BoolVar(&filter.Overdue, "overdue", false, "Show only past-due open reminders")
	cmd.Flags().IntVar(&filter.Upcoming, "upcoming", 0, "Show open reminders due within the next N days")
	cmd.Flags().IntVar(&filter.Limit, "limit", 100, "Maximum reminders to list")
	return cmd
}

func newRemindersGetCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id-or-uuid-prefix>",
		Short: "Show a single reminder's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openReminderStore()
			if err != nil {
				return err
			}
			defer store.Close()

			r, err := store.GetByAny(args[0])
			if err != nil {
				return err
			}
			if r == nil {
				return configErr(fmt.Errorf("reminder not found: %s", args[0]))
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, r)
			}
			printReminderDetail(f, out, r)
			return nil
		},
	}
	return cmd
}

func newRemindersSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across reminder titles, notes, and lists",
		Args:  cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli reminders search milk
  icloud-pp-cli reminders search "call dentist"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openReminderStore()
			if err != nil {
				return err
			}
			defer store.Close()

			query := joinArgs(args)
			rs, err := store.Search(query, limit)
			if err != nil {
				return err
			}
			if len(rs) == 0 {
				fmt.Fprintf(out, "No reminders match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, rs)
			}
			printRemindersTable(f, out, rs)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(rs))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	return cmd
}

func newRemindersAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "Overview stats and per-list breakdown",
		Example: `  icloud-pp-cli reminders analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openReminderStore()
			if err != nil {
				return err
			}
			defer store.Close()

			ov, err := store.Overview()
			if err != nil {
				return err
			}
			lists, err := store.AnalyticsLists()
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview *RemindersOverview `json:"overview"`
					Lists    []ListCount        `json:"lists"`
				}{ov, lists})
			}
			printRemindersAnalytics(f, out, ov, lists)
			return nil
		},
	}
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printRemindersTable(f *rootFlags, out io.Writer, rs []Reminder) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "")+"\t"+bold(f, out, "Reminder")+"\t"+bold(f, out, "List")+"\t"+bold(f, out, "Due")+"\t"+bold(f, out, "Pri"))
	now := time.Now()
	for _, r := range rs {
		check := "○"
		if r.Completed {
			check = "✓"
		}
		due := shortDate(r.DueDate)
		// Flag overdue open items in red.
		if !r.Completed && r.DueDate != "" {
			if t, err := time.Parse(time.RFC3339, r.DueDate); err == nil && t.Before(now) {
				due = red(f, out, due+" ⚠")
			}
		}
		pri := r.PriorityLabel
		if r.Flagged {
			pri = "⚑ " + pri
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			check, truncate(r.Name, 40), truncate(r.List, 16), due, pri)
	}
	tw.Flush()
}

func printReminderDetail(f *rootFlags, out io.Writer, r *Reminder) {
	check := "○ open"
	if r.Completed {
		check = "✓ completed"
	}
	fmt.Fprintln(out, bold(f, out, r.Name))
	fmt.Fprintf(out, "  %s · list: %s\n", check, r.List)
	if r.DueDate != "" {
		fmt.Fprintf(out, "  due: %s\n", shortDateTime(r.DueDate))
	}
	if r.RemindDate != "" {
		fmt.Fprintf(out, "  remind: %s\n", shortDateTime(r.RemindDate))
	}
	if r.PriorityLabel != "" {
		fmt.Fprintf(out, "  priority: %s\n", r.PriorityLabel)
	}
	if r.Flagged {
		fmt.Fprintln(out, "  ⚑ flagged")
	}
	if r.CompletionDate != "" {
		fmt.Fprintf(out, "  completed: %s\n", shortDateTime(r.CompletionDate))
	}
	if r.Body != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, r.Body)
	}
}

func printRemindersAnalytics(f *rootFlags, out io.Writer, o *RemindersOverview, lists []ListCount) {
	fmt.Fprintln(out, bold(f, out, "Reminders overview"))
	fmt.Fprintf(out, "  %s total · %s open · %s completed\n",
		formatInt(o.Total), formatInt(o.Open), formatInt(o.Completed))
	fmt.Fprintf(out, "  %s overdue · %s due today · %s due this week · %s no due date\n",
		formatInt(o.Overdue), formatInt(o.DueToday), formatInt(o.DueThisWeek), formatInt(o.NoDueDate))
	fmt.Fprintf(out, "  %s high-priority open · %s flagged open\n",
		formatInt(o.HighPriority), formatInt(o.Flagged))
	fmt.Fprintln(out)

	if len(lists) > 0 {
		fmt.Fprintln(out, bold(f, out, "By list"))
		tw := newTabWriter(out)
		fmt.Fprintln(tw, bold(f, out, "List")+"\t"+bold(f, out, "Total")+"\t"+bold(f, out, "Open")+"\t"+bold(f, out, "Done")+"\t"+bold(f, out, "Overdue"))
		for _, lc := range lists {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\n", truncate(lc.List, 28), lc.Total, lc.Open, lc.Completed, lc.Overdue)
		}
		tw.Flush()
	}
}
