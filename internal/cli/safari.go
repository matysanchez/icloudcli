// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — safari.go
// `safari` command group: read Safari's local browsing data — history (from
// History.db) and bookmarks (from Bookmarks.plist) — read-only. History needs
// Full Disk Access; run `icloud-pp-cli doctor` if access is denied.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

func newSafariCmd(f *rootFlags) *cobra.Command {
	safari := &cobra.Command{
		Use:   "safari",
		Short: "Read your Safari history and bookmarks",
		Long: `Read Safari's local data: browsing history (History.db) and bookmarks
(Bookmarks.plist). All access is read-only and runs directly against Safari's
own files — there is no sync step.

History requires Full Disk Access for your terminal. If history commands fail
with a permission error, run "icloud-pp-cli doctor" for remediation steps.`,
	}

	safari.AddCommand(newSafariHistoryCmd(f))
	safari.AddCommand(newSafariSearchCmd(f))
	safari.AddCommand(newSafariTopSitesCmd(f))
	safari.AddCommand(newSafariBookmarksCmd(f))
	safari.AddCommand(newSafariReadingListCmd(f))
	safari.AddCommand(newSafariAnalyticsCmd(f))

	return safari
}

func newSafariReadingListCmd(f *rootFlags) *cobra.Command {
	var unreadOnly bool
	cmd := &cobra.Command{
		Use:     "reading-list",
		Aliases: []string{"readinglist"},
		Short:   "List your Safari Reading List (saved-for-later articles)",
		Example: `  icloud-pp-cli safari reading-list
  icloud-pp-cli safari reading-list --unread
  icloud-pp-cli safari reading-list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			items, err := readReadingList("")
			if err != nil {
				return err
			}
			if unreadOnly {
				filtered := items[:0]
				for _, it := range items {
					if it.Unread {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "Reading List is empty.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, items)
			}
			printReadingList(f, out, items)
			fmt.Fprintf(out, "\n%s items\n", formatInt(int64(len(items))))
			return nil
		},
	}
	cmd.Flags().BoolVar(&unreadOnly, "unread", false, "Show only unread items")
	return cmd
}

func newSafariHistoryCmd(f *rootFlags) *cobra.Command {
	var filter HistoryFilter
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List recently visited pages (most recent first)",
		Example: `  icloud-pp-cli safari history
  icloud-pp-cli safari history --limit 50
  icloud-pp-cli safari history --domain github.com
  icloud-pp-cli safari history --since 2026-06-01`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if sinceStr != "" {
				t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
				if err != nil {
					return usageErr(fmt.Errorf("--since: expected YYYY-MM-DD, got %q", sinceStr))
				}
				filter.Since = t
			}
			db, err := openSafariHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			visits, err := queryHistory(db, filter)
			if err != nil {
				return err
			}
			if len(visits) == 0 {
				fmt.Fprintln(out, "No history entries match.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, visits)
			}
			printVisitsTable(f, out, visits)
			fmt.Fprintf(out, "\n%s entries\n", formatInt(int64(len(visits))))
			return nil
		},
	}
	cmd.Flags().IntVar(&filter.Limit, "limit", 100, "Maximum entries")
	cmd.Flags().StringVar(&filter.Domain, "domain", "", "Filter to URLs matching this domain substring")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Only visits on or after this date (YYYY-MM-DD)")
	return cmd
}

func newSafariSearchCmd(f *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search history by URL and page title",
		Args:  cobra.MinimumNArgs(1),
		Example: `  icloud-pp-cli safari search "pull request"
  icloud-pp-cli safari search recipe --limit 30`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openSafariHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			query := joinArgs(args)
			visits, err := searchHistory(db, query, limit)
			if err != nil {
				return err
			}
			if len(visits) == 0 {
				fmt.Fprintf(out, "No history matches %q.\n", query)
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, visits)
			}
			printVisitsTable(f, out, visits)
			fmt.Fprintf(out, "\n%s matches for %q\n", formatInt(int64(len(visits))), query)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum results")
	return cmd
}

func newSafariTopSitesCmd(f *rootFlags) *cobra.Command {
	var limit int
	var sinceStr string
	cmd := &cobra.Command{
		Use:   "top-sites",
		Short: "Most-visited domains by visit count",
		Example: `  icloud-pp-cli safari top-sites
  icloud-pp-cli safari top-sites --limit 50 --since 2026-01-01`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var since time.Time
			if sinceStr != "" {
				t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
				if err != nil {
					return usageErr(fmt.Errorf("--since: expected YYYY-MM-DD, got %q", sinceStr))
				}
				since = t
			}
			db, err := openSafariHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			doms, err := topDomains(db, since, limit)
			if err != nil {
				return err
			}
			if len(doms) == 0 {
				fmt.Fprintln(out, "No domains found.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, doms)
			}
			printDomainsTable(f, out, doms)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 25, "Maximum domains")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Only count visits on or after this date (YYYY-MM-DD)")
	return cmd
}

func newSafariBookmarksCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bookmarks",
		Short: "List your Safari bookmarks (flattened, with folder path)",
		Example: `  icloud-pp-cli safari bookmarks
  icloud-pp-cli safari bookmarks --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			bms, err := readBookmarks("")
			if err != nil {
				return err
			}
			if len(bms) == 0 {
				fmt.Fprintln(out, "No bookmarks found.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, bms)
			}
			printBookmarksTable(f, out, bms)
			fmt.Fprintf(out, "\n%s bookmarks\n", formatInt(int64(len(bms))))
			return nil
		},
	}
	return cmd
}

func newSafariAnalyticsCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "analytics",
		Short:   "History overview + top domains",
		Example: `  icloud-pp-cli safari analytics`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			db, err := openSafariHistory("")
			if err != nil {
				return err
			}
			defer db.Close()

			ov, err := safariOverview(db)
			if err != nil {
				return err
			}
			doms, err := topDomains(db, time.Time{}, 10)
			if err != nil {
				return err
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, struct {
					Overview   *SafariOverview `json:"overview"`
					TopDomains []DomainVisit   `json:"top_domains"`
				}{ov, doms})
			}
			fmt.Fprintln(out, bold(f, out, "Safari history overview"))
			fmt.Fprintf(out, "  %s URLs · %s visits · %s domains\n",
				formatInt(ov.TotalURLs), formatInt(ov.TotalVisits), formatInt(ov.DistinctDoms))
			fmt.Fprintf(out, "  span: %s → %s\n", shortDate(ov.OldestVisit), shortDate(ov.NewestVisit))
			fmt.Fprintln(out)
			if len(doms) > 0 {
				fmt.Fprintln(out, bold(f, out, "Top domains"))
				printDomainsTable(f, out, doms)
			}
			return nil
		},
	}
	return cmd
}

// ── bookmarks parsing ─────────────────────────────────────────────────────────

// Bookmark is a flattened Safari bookmark with its folder path.
type Bookmark struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Folder string `json:"folder,omitempty"`
}

// plistNode mirrors the recursive Bookmarks.plist shape after plutil JSON
// conversion: a node is either a list (WebBookmarkTypeList, with Children) or a
// leaf (WebBookmarkTypeLeaf, with URLString + URIDictionary.title). Reading List
// leaves additionally carry a "ReadingList" dict (DateAdded, PreviewText) and,
// once opened, a "ReadingListNonSync" dict with DateLastViewed.
type plistNode struct {
	WebBookmarkType    string                 `json:"WebBookmarkType"`
	Title              string                 `json:"Title"`
	URLString          string                 `json:"URLString"`
	URIDictionary      map[string]interface{} `json:"URIDictionary"`
	ReadingList        map[string]interface{} `json:"ReadingList"`
	ReadingListNonSync map[string]interface{} `json:"ReadingListNonSync"`
	Children           []plistNode            `json:"Children"`
}

// readBookmarks converts Bookmarks.plist to JSON via plutil and flattens the
// tree to a list of leaf bookmarks. Using plutil avoids adding a plist-parsing
// dependency and handles both binary and XML plist encodings.
func readBookmarks(path string) ([]Bookmark, error) {
	if path == "" {
		path = defaultSafariBookmarksPath()
	}
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", path)
	data, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, configErr(fmt.Errorf("reading bookmarks via plutil failed (Full Disk Access may be required): %s",
				string(ee.Stderr)))
		}
		return nil, fmt.Errorf("running plutil: %w", err)
	}
	var root plistNode
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing bookmarks JSON: %w", err)
	}
	var out []Bookmark
	flattenBookmarks(root, "", &out)
	return out, nil
}

// flattenBookmarks walks the bookmark tree depth-first, accumulating leaves with
// their slash-joined folder path. The synthetic root and reading-list container
// titles are not prepended to keep paths clean.
func flattenBookmarks(node plistNode, folder string, out *[]Bookmark) {
	switch node.WebBookmarkType {
	case "WebBookmarkTypeLeaf":
		title := ""
		if node.URIDictionary != nil {
			if t, ok := node.URIDictionary["title"].(string); ok {
				title = t
			}
		}
		if title == "" {
			title = node.URLString
		}
		*out = append(*out, Bookmark{Title: title, URL: node.URLString, Folder: folder})
	default:
		// List (or root). Compute the child folder path from this node's title,
		// skipping the empty/root title.
		childFolder := folder
		if node.Title != "" && node.Title != "BookmarksBar" && node.Title != "BookmarksMenu" {
			if folder == "" {
				childFolder = node.Title
			} else {
				childFolder = folder + "/" + node.Title
			}
		}
		for _, c := range node.Children {
			flattenBookmarks(c, childFolder, out)
		}
	}
}

// ── reading list ──────────────────────────────────────────────────────────────

// ReadingListItem is one saved Reading List entry.
type ReadingListItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Added   string `json:"added,omitempty"`
	Preview string `json:"preview,omitempty"`
	Unread  bool   `json:"unread"`
}

// readReadingList parses Bookmarks.plist and returns the Reading List entries.
// Reading List leaves are identified by the presence of a "ReadingList" dict.
func readReadingList(path string) ([]ReadingListItem, error) {
	if path == "" {
		path = defaultSafariBookmarksPath()
	}
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", path)
	data, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, configErr(fmt.Errorf("reading bookmarks via plutil failed (Full Disk Access may be required): %s",
				string(ee.Stderr)))
		}
		return nil, fmt.Errorf("running plutil: %w", err)
	}
	var root plistNode
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing bookmarks JSON: %w", err)
	}
	var out []ReadingListItem
	collectReadingList(root, &out)
	// Newest first by DateAdded string (RFC3339-ish sorts lexically).
	sort.Slice(out, func(i, j int) bool { return out[i].Added > out[j].Added })
	return out, nil
}

// collectReadingList walks the tree and appends every leaf carrying a ReadingList
// dict, recording when it was added, its preview text, and whether it's unread.
func collectReadingList(node plistNode, out *[]ReadingListItem) {
	if node.WebBookmarkType == "WebBookmarkTypeLeaf" && node.ReadingList != nil {
		item := ReadingListItem{URL: node.URLString}
		if node.URIDictionary != nil {
			if t, ok := node.URIDictionary["title"].(string); ok {
				item.Title = t
			}
		}
		if item.Title == "" {
			item.Title = node.URLString
		}
		if v, ok := node.ReadingList["DateAdded"].(string); ok {
			item.Added = v
		}
		if v, ok := node.ReadingList["PreviewText"].(string); ok {
			item.Preview = v
		}
		// Unread = never viewed (no DateLastViewed in the non-sync dict).
		item.Unread = true
		if node.ReadingListNonSync != nil {
			if _, ok := node.ReadingListNonSync["DateLastViewed"]; ok {
				item.Unread = false
			}
		}
		*out = append(*out, item)
	}
	for _, c := range node.Children {
		collectReadingList(c, out)
	}
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printReadingList(f *rootFlags, out io.Writer, items []ReadingListItem) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Added")+"\t"+bold(f, out, "")+"\t"+bold(f, out, "Title")+"\t"+bold(f, out, "URL"))
	for _, it := range items {
		mark := " "
		if it.Unread {
			mark = green(f, out, "●") // unread dot
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			shortDate(it.Added), mark, truncate(it.Title, 38), truncate(it.URL, 44))
	}
	tw.Flush()
}

func printVisitsTable(f *rootFlags, out io.Writer, visits []HistoryVisit) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Last visit")+"\t"+bold(f, out, "Visits")+"\t"+bold(f, out, "Title")+"\t"+bold(f, out, "URL"))
	for _, v := range visits {
		title := v.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n",
			shortDateTime(v.LastVisit), v.VisitCount, truncate(title, 32), truncate(v.URL, 50))
	}
	tw.Flush()
}

func printDomainsTable(f *rootFlags, out io.Writer, doms []DomainVisit) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Domain")+"\t"+bold(f, out, "Visits")+"\t"+bold(f, out, "URLs"))
	for _, d := range doms {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", truncate(d.Domain, 40), d.VisitCount, d.URLs)
	}
	tw.Flush()
}

func printBookmarksTable(f *rootFlags, out io.Writer, bms []Bookmark) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Folder")+"\t"+bold(f, out, "Title")+"\t"+bold(f, out, "URL"))
	for _, b := range bms {
		folder := b.Folder
		if folder == "" {
			folder = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", truncate(folder, 20), truncate(b.Title, 34), truncate(b.URL, 46))
	}
	tw.Flush()
}
