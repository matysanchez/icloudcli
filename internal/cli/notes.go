// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — notes.go
// `notes` command group: read your Apple Notes (iCloud-synced) from the local
// Notes.app via JXA into a SQLite cache, then list / get / search / analyze
// them instantly. Read-only — never creates, edits, or deletes notes.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

func newNotesCmd(f *rootFlags) *cobra.Command {
	notes := &cobra.Command{
		Use:   "notes",
		Short: "Read and search your Apple Notes",
		Long: `Read your Apple Notes (iCloud-synced) via Notes.app.

A one-time 'notes sync' pulls every note into a local SQLite cache via
JavaScript for Automation (JXA); list, get, search, and analytics then run
instantly against the cache. All operations are read-only.

Requires Automation permission for your terminal to control Notes.app
(macOS prompts on first sync). Password-protected notes expose only their
title and metadata — their body is not readable via automation.`,
	}

	notes.AddCommand(newNotesSyncCmd(f))
	notes.AddCommand(newNotesListCmd(f))
	notes.AddCommand(newNotesGetCmd(f))
	notes.AddCommand(newNotesSearchCmd(f))
	notes.AddCommand(newNotesAnalyticsCmd(f))

	return notes
}

// jxaNotesSyncScript exports every note via JXA. It walks accounts → folders so
// each note carries both its folder and account name. Bodies are pulled as
// plaintext where available, falling back to the HTML body with tags stripped.
const jxaNotesSyncScript = `
function run() {
  var app = Application("Notes");
  var out = [];
  function stripTags(s) {
    if (!s) return "";
    return s.replace(/<[^>]+>/g, " ").replace(/&nbsp;/g, " ")
            .replace(/&amp;/g, "&").replace(/&lt;/g, "<").replace(/&gt;/g, ">")
            .replace(/\s+/g, " ").trim();
  }
  var accounts;
  try { accounts = app.accounts(); } catch (e) { accounts = []; }
  for (var ai = 0; ai < accounts.length; ai++) {
    var acct = accounts[ai];
    var acctName = "";
    try { acctName = acct.name(); } catch (e) {}
    var folders;
    try { folders = acct.folders(); } catch (e) { folders = []; }
    for (var fi = 0; fi < folders.length; fi++) {
      var folder = folders[fi];
      var folderName = "";
      try { folderName = folder.name(); } catch (e) {}
      var notes;
      try { notes = folder.notes(); } catch (e) { notes = []; }
      for (var ni = 0; ni < notes.length; ni++) {
        var n = notes[ni];
        var body = "";
        var locked = false;
        try { locked = n.passwordProtected(); } catch (e) {}
        if (!locked) {
          try { body = n.plaintext(); } catch (e) {
            try { body = stripTags(n.body()); } catch (e2) {}
          }
        }
        var shared = false;
        try { shared = n.shared(); } catch (e) {}
        out.push({
          id:           n.id(),
          name:         (function(){ try { return n.name(); } catch(e){ return ""; } })(),
          body:         body || "",
          folder:       folderName,
          account:      acctName,
          shared:       shared,
          pwdProtected: locked,
          createdAt:    (function(){ try { return n.creationDate().toISOString(); } catch(e){ return null; } })(),
          modifiedAt:   (function(){ try { return n.modificationDate().toISOString(); } catch(e){ return null; } })()
        });
      }
    }
  }
  return JSON.stringify(out);
}
`

func newNotesSyncCmd(f *rootFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync notes from Notes.app into the local cache",
		Long: `Pulls all notes from Notes.app via JXA into a local SQLite database
for instant querying. Run once before list/search/analytics; re-run to
pick up new or edited notes.`,
		Example: `  icloud-pp-cli notes sync
  icloud-pp-cli notes sync --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openNoteStore()
			if err != nil {
				return err
			}
			defer store.Close()

			if !force {
				if last := store.LastSyncedAt(); last != "" {
					fmt.Fprintf(out, "  %s Last synced: %s\n", yellow(f, out, "i"), last)
					fmt.Fprintln(out, "  Use --force to re-sync.")
					count, _ := store.Count()
					fmt.Fprintf(out, "  %s %s notes in local store.\n", green(f, out, "✓"), formatInt(int64(count)))
					return nil
				}
			}

			fmt.Fprintln(out, bold(f, out, "Syncing notes from Notes.app..."))
			fmt.Fprintln(out, "  → Fetching notes via JXA (may prompt for Automation access)...")
			start := time.Now()

			raw, err := runJXAForApp("Notes", jxaNotesSyncScript)
			if err != nil {
				if isAutomationDenied(err) {
					return configErr(fmt.Errorf("Notes automation denied.\n%s", automationHint("Notes")))
				}
				return fmt.Errorf("JXA sync failed: %w", err)
			}

			var notes []jxaNote
			if err := json.Unmarshal([]byte(raw), &notes); err != nil {
				return fmt.Errorf("parsing JXA output: %w", err)
			}
			fmt.Fprintf(out, "  → Fetched %s notes\n", formatInt(int64(len(notes))))

			n, err := store.SyncAll(notes)
			if err != nil {
				return fmt.Errorf("storing notes: %w", err)
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Fprintln(out)
			fmt.Fprintf(out, "%s %s notes synced in %s\n", green(f, out, "✓"), formatInt(int64(n)), elapsed)
			fmt.Fprintf(out, "    DB: %s\n", notesDBPath())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-sync even if already synced")
	return cmd
}

func newNotesListCmd(f *rootFlags) *cobra.Command {
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes (most recently modified first)",
		Example: `  icloud-pp-cli notes list
  icloud-pp-cli notes list --limit 100
  icloud-pp-cli notes list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openNoteStore()
			if err != nil {
				return err
			}
			defer store.Close()

			ns, err := store.List(limit, offset)
			if err != nil {
				return err
			}
			if len(ns) == 0 {
				fmt.Fprintln(out, "No notes in local store. Run: icloud-pp-cli notes sync")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, ns)
			}
			printNotesTable(f, out, ns)
			total, _ := store.Count()
			fmt.Fprintf(out, "\n%s of %s notes\n", formatInt(int64(len(ns))), formatInt(int64(total)))
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum notes to list")
	cmd.Flags().IntVar(&offset, "offset", 0, "Number of notes to skip")
	return cmd
}

func newNotesGetCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id-or-uuid-prefix>",
		Short: "Show a single note's full body",
		Args:  cobra.ExactArgs(1),
		Example: `  icloud-pp-cli notes get p123
  icloud-pp-cli notes get p123 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openNoteStore()
			if err != nil {
				return err
			}
			defer store.Close()

			n, err := store.GetByAny(args[0])
			if err != nil {
				return err
			}
			if n == nil {
				return configErr(fmt.Errorf("note not found: %s", args[0]))
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, n)
			}
			printNoteDetail(f, out, n)
			return nil
		},
	}
	return cmd
}

func newNotesSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across note titles and bodies",
		Args:  cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli notes search recipe
  icloud-pp-cli notes search "tax 2025"
  icloud-pp-cli notes search invoice --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openNoteStore()
			if err != nil {
				return err
			}
			defer store.Close()

			query := joinArgs(args)
			ns, err := store.Search(query, limit)
			if err != nil {
				return err
			}
			if len(ns) == 0 {
				fmt.Fprintf(out, "No notes match %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, ns)
			}
			printNotesTable(f, out, ns)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(ns))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results")
	return cmd
}

func newNotesAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analytics",
		Short: "Overview stats and per-folder breakdown",
		Example: `  icloud-pp-cli notes analytics
  icloud-pp-cli notes analytics --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openNoteStore()
			if err != nil {
				return err
			}
			defer store.Close()

			ov, err := store.Overview()
			if err != nil {
				return err
			}
			folders, err := store.AnalyticsFolders()
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview *NotesOverview `json:"overview"`
					Folders  []FolderCount  `json:"folders"`
				}{ov, folders})
			}
			printNotesAnalytics(f, out, ov, folders)
			return nil
		},
	}
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printNotesTable(f *rootFlags, out io.Writer, ns []Note) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "ID")+"\t"+bold(f, out, "Title")+"\t"+bold(f, out, "Folder")+"\t"+bold(f, out, "Words")+"\t"+bold(f, out, "Modified"))
	for _, n := range ns {
		title := n.Name
		if title == "" {
			title = "(untitled)"
		}
		lock := ""
		if n.PwdProtected {
			lock = " 🔒"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			shortID(n.UUID), truncate(title, 40)+lock, truncate(n.Folder, 18), n.WordCount, shortDate(n.ModifiedAt))
	}
	tw.Flush()
}

func printNoteDetail(f *rootFlags, out io.Writer, n *Note) {
	title := n.Name
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintln(out, bold(f, out, title))
	loc := n.Folder
	if n.Account != "" {
		loc = n.Account + " › " + n.Folder
	}
	fmt.Fprintf(out, "  %s · %d words · %d chars\n", loc, n.WordCount, n.CharCount)
	fmt.Fprintf(out, "  created %s · modified %s\n", shortDate(n.CreatedAt), shortDate(n.ModifiedAt))
	if n.Shared {
		fmt.Fprintln(out, "  "+yellow(f, out, "shared note"))
	}
	fmt.Fprintln(out)
	if n.PwdProtected {
		fmt.Fprintln(out, yellow(f, out, "  🔒 Password-protected — body not available via automation."))
		return
	}
	fmt.Fprintln(out, n.Body)
}

func printNotesAnalytics(f *rootFlags, out io.Writer, o *NotesOverview, folders []FolderCount) {
	fmt.Fprintln(out, bold(f, out, "Notes overview"))
	fmt.Fprintf(out, "  %s notes · %s words · %s chars · avg %.0f words/note\n",
		formatInt(o.TotalNotes), formatInt(o.TotalWords), formatInt(o.TotalChars), o.AvgWords)
	fmt.Fprintf(out, "  %s shared · %s locked · %s empty\n",
		formatInt(o.SharedNotes), formatInt(o.LockedNotes), formatInt(o.EmptyNotes))
	if o.LongestName != "" {
		fmt.Fprintf(out, "  longest: %q (%s words)\n", truncate(o.LongestName, 40), formatInt(o.LongestWords))
	}
	fmt.Fprintln(out)

	if len(folders) > 0 {
		fmt.Fprintln(out, bold(f, out, "By folder"))
		tw := newTabWriter(out)
		fmt.Fprintln(tw, bold(f, out, "Folder")+"\t"+bold(f, out, "Account")+"\t"+bold(f, out, "Notes")+"\t"+bold(f, out, "Words"))
		for _, fc := range folders {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", truncate(fc.Folder, 28), truncate(fc.Account, 18), fc.Count, fc.Words)
		}
		tw.Flush()
	}
}
