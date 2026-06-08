// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — drive.go
// `drive` command group: inspect iCloud Drive (~/Library/Mobile Documents/)
// directly on the filesystem. Reports what's stored, how big it is, and crucially
// which files are downloaded locally vs cloud-only (dataless) — the question that
// actually matters for "what's using my iCloud storage / my disk". Read-only.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

func newDriveCmd(f *rootFlags) *cobra.Command {
	drive := &cobra.Command{
		Use:   "drive",
		Short: "Inspect iCloud Drive — usage, files, and download status",
		Long: `Inspect iCloud Drive (~/Library/Mobile Documents/) directly on disk.

Every file reports its logical size and whether it is downloaded locally or
stored only in the cloud (a "dataless" placeholder that takes no disk space
until opened). No special permission is required — these are your own files.`,
	}

	drive.AddCommand(newDriveStatusCmd(f))
	drive.AddCommand(newDriveUsageCmd(f))
	drive.AddCommand(newDriveListCmd(f))

	return drive
}

func mobileDocumentsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Mobile Documents")
}

func cloudDocsPath() string {
	return filepath.Join(mobileDocumentsPath(), "com~apple~CloudDocs")
}

// ── types ─────────────────────────────────────────────────────────────────────

// DriveEntry is one file or directory in iCloud Drive.
type DriveEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	SizeBytes  int64  `json:"size_bytes"`
	Downloaded bool   `json:"downloaded"` // false = cloud-only (dataless / .icloud placeholder)
}

// ContainerUsage aggregates one top-level app container under Mobile Documents.
type ContainerUsage struct {
	App             string `json:"app"`
	Files           int64  `json:"files"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	CloudOnlyBytes  int64  `json:"cloud_only_bytes"`
	CloudOnlyFiles  int64  `json:"cloud_only_files"`
}

// TotalBytes is downloaded + cloud-only.
func (c ContainerUsage) TotalBytes() int64 { return c.DownloadedBytes + c.CloudOnlyBytes }

// DriveStatus is the headline summary across all containers.
type DriveStatus struct {
	TotalFiles      int64            `json:"total_files"`
	DownloadedFiles int64            `json:"downloaded_files"`
	CloudOnlyFiles  int64            `json:"cloud_only_files"`
	DownloadedBytes int64            `json:"downloaded_bytes"`
	CloudOnlyBytes  int64            `json:"cloud_only_bytes"`
	Containers      []ContainerUsage `json:"containers,omitempty"`
}

// ── walk ──────────────────────────────────────────────────────────────────────

// scanContainers walks every top-level container under Mobile Documents and
// aggregates per-container usage plus an overall total.
func scanContainers() (*DriveStatus, error) {
	root := mobileDocumentsPath()
	top, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("iCloud Drive not found at %s (is iCloud Drive enabled?)", root)
		}
		return nil, err
	}
	var status DriveStatus
	for _, d := range top {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		usage := ContainerUsage{App: friendlyContainer(d.Name())}
		walkAggregate(filepath.Join(root, d.Name()), &usage)
		if usage.Files == 0 {
			continue
		}
		status.Containers = append(status.Containers, usage)
		status.TotalFiles += usage.Files
		status.CloudOnlyFiles += usage.CloudOnlyFiles
		status.DownloadedFiles += usage.Files - usage.CloudOnlyFiles
		status.DownloadedBytes += usage.DownloadedBytes
		status.CloudOnlyBytes += usage.CloudOnlyBytes
	}
	sort.Slice(status.Containers, func(i, j int) bool {
		return status.Containers[i].TotalBytes() > status.Containers[j].TotalBytes()
	})
	return &status, nil
}

// walkAggregate accumulates file counts and sizes under dir into usage.
func walkAggregate(dir string, usage *ContainerUsage) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".DS_Store" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		usage.Files++
		size, downloaded := fileSizeAndStatus(name, info)
		if downloaded {
			usage.DownloadedBytes += size
		} else {
			usage.CloudOnlyBytes += size
			usage.CloudOnlyFiles++
		}
		return nil
	})
}

// listDriveDir returns the immediate entries of a directory inside iCloud Drive.
func listDriveDir(dir string) ([]DriveEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []DriveEntry
	for _, d := range entries {
		name := d.Name()
		if name == ".DS_Store" {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		e := DriveEntry{Name: cloudLogicalName(name), IsDir: d.IsDir()}
		if d.IsDir() {
			e.Downloaded = true
		} else {
			e.SizeBytes, e.Downloaded = fileSizeAndStatus(name, info)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // dirs first
		}
		return out[i].SizeBytes > out[j].SizeBytes
	})
	return out, nil
}

// fileSizeAndStatus returns a file's logical size and whether it's downloaded.
// Two mechanisms: legacy ".<name>.icloud" placeholder files (always cloud-only),
// and modern APFS dataless files (real path, full logical size, but zero blocks
// allocated on disk). st_blocks == 0 with a non-zero size means not downloaded.
func fileSizeAndStatus(name string, info os.FileInfo) (size int64, downloaded bool) {
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".icloud") {
		// Legacy placeholder; the small plist's own size is meaningless, and the
		// real size isn't cheaply available, so report 0 and mark cloud-only.
		return 0, false
	}
	size = info.Size()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		// Blocks is in 512-byte units. Zero blocks for a non-empty file = dataless.
		if size > 0 && st.Blocks == 0 {
			return size, false
		}
	}
	return size, true
}

// cloudLogicalName strips the ".<name>.icloud" placeholder wrapping to show the
// real filename; other names pass through unchanged.
func cloudLogicalName(name string) string {
	if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".icloud") {
		return strings.TrimSuffix(strings.TrimPrefix(name, "."), ".icloud")
	}
	return name
}

// friendlyContainer turns a mangled Mobile Documents container name into a
// readable app name: "com~apple~CloudDocs" → "iCloud Drive",
// "57T9237FN3~net~whatsapp~WhatsApp" → "WhatsApp", "iCloud~md~obsidian" → "obsidian".
func friendlyContainer(mangled string) string {
	if mangled == "com~apple~CloudDocs" {
		return "iCloud Drive"
	}
	name := strings.ReplaceAll(mangled, "~", ".")
	name = strings.TrimPrefix(name, "iCloud.")
	parts := strings.Split(name, ".")
	// Drop a leading 10-char team-ID segment (uppercase letters + digits).
	if len(parts) > 1 && isTeamID(parts[0]) {
		parts = parts[1:]
	}
	// Use the last segment as the display name — it's the app/bundle leaf.
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return name
}

// isTeamID reports whether s looks like an Apple Developer team ID: exactly 10
// characters of uppercase letters and digits.
func isTeamID(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// ── commands ──────────────────────────────────────────────────────────────────

func newDriveStatusCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Summary of iCloud Drive usage and download state",
		Example: `  icloud-pp-cli drive status
  icloud-pp-cli drive status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			status, err := scanContainers()
			if err != nil {
				return configErr(err)
			}
			if f.asJSON || !isTerminal(out) {
				// Trim per-container detail in compact mode; status is a summary.
				if f.compact {
					status.Containers = nil
				}
				return printJSON(out, status)
			}
			fmt.Fprintln(out, bold(f, out, "iCloud Drive"))
			fmt.Fprintf(out, "  %s files · %s downloaded · %s cloud-only\n",
				formatInt(status.TotalFiles), formatInt(status.DownloadedFiles), formatInt(status.CloudOnlyFiles))
			fmt.Fprintf(out, "  %s on this Mac · %s in the cloud only\n",
				humanBytes(status.DownloadedBytes), humanBytes(status.CloudOnlyBytes))
			fmt.Fprintln(out)
			topN := status.Containers
			if len(topN) > 10 {
				topN = topN[:10]
			}
			fmt.Fprintln(out, bold(f, out, "Top containers"))
			printContainerTable(f, out, topN)
			return nil
		},
	}
	return cmd
}

func newDriveUsageCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "usage",
		Short:   "Per-app storage breakdown across iCloud Drive",
		Example: `  icloud-pp-cli drive usage`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			status, err := scanContainers()
			if err != nil {
				return configErr(err)
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, status.Containers)
			}
			fmt.Fprintln(out, bold(f, out, "iCloud Drive usage by app"))
			printContainerTable(f, out, status.Containers)
			return nil
		},
	}
	return cmd
}

func newDriveListCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [subpath]",
		Short: "List entries in iCloud Drive (the CloudDocs root by default)",
		Args:  cobra.MaximumNArgs(1),
		Example: `  icloud-pp-cli drive list
  icloud-pp-cli drive list Documents
  icloud-pp-cli drive list Downloads --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			base := cloudDocsPath()
			if _, err := os.Stat(base); err != nil {
				// Fall back to the Mobile Documents root if CloudDocs is absent.
				base = mobileDocumentsPath()
			}
			target := base
			if len(args) == 1 {
				clean := filepath.Clean("/" + args[0]) // prevent ../ escape
				target = filepath.Join(base, clean)
			}
			entries, err := listDriveDir(target)
			if err != nil {
				return configErr(fmt.Errorf("cannot list %s: %w", target, err))
			}
			if len(entries) == 0 {
				fmt.Fprintln(out, "Empty.")
				return nil
			}
			if f.asJSON || !isTerminal(out) {
				return printJSON(out, entries)
			}
			printDriveEntries(f, out, entries)
			return nil
		},
	}
	return cmd
}

// ── rendering ─────────────────────────────────────────────────────────────────

func printContainerTable(f *rootFlags, out io.Writer, containers []ContainerUsage) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "App")+"\t"+bold(f, out, "Files")+"\t"+bold(f, out, "On Mac")+"\t"+bold(f, out, "Cloud-only"))
	for _, c := range containers {
		cloud := fmt.Sprintf("%s (%d)", humanBytes(c.CloudOnlyBytes), c.CloudOnlyFiles)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", truncate(c.App, 32), c.Files, humanBytes(c.DownloadedBytes), cloud)
	}
	tw.Flush()
}

func printDriveEntries(f *rootFlags, out io.Writer, entries []DriveEntry) {
	tw := newTabWriter(out)
	fmt.Fprintln(tw, bold(f, out, "Name")+"\t"+bold(f, out, "Size")+"\t"+bold(f, out, "State"))
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(tw, "%s/\t%s\t%s\n", truncate(e.Name, 44), "—", "dir")
			continue
		}
		state := green(f, out, "local")
		if !e.Downloaded {
			state = yellow(f, out, "cloud")
		}
		size := humanBytes(e.SizeBytes)
		if e.SizeBytes == 0 && !e.Downloaded {
			size = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", truncate(e.Name, 44), size, state)
	}
	tw.Flush()
}

// humanBytes renders a byte count in B/KB/MB/GB/TB with one decimal place.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
