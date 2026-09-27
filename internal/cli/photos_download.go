// Copyright 2026 mvanhorn. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newDownloadCmd(f *rootFlags) *cobra.Command {
	var outputDir string
	var sensitive bool
	var confirm bool
	var mediaType string
	var limit int
	var batchSize int

	cmd := &cobra.Command{
		Use:   "download [uuid...]",
		Short: "Export originals from iCloud to a local folder",
		Long: `Export original photos or videos from iCloud Photos to a local folder.

Photos.app is used to perform the export. If a file is not stored locally
(iCloud optimized storage), Photos.app downloads the original from iCloud
automatically before copying it to the output directory.

Pass UUIDs explicitly, or use --sensitive to target items Apple's on-device
ML engine has flagged as containing nudity.

Get UUIDs from any read command:
  icloud-pp-cli photos top --json | jq -r '.[].uuid'
  icloud-pp-cli photos videos --json | jq -r '.[].uuid'`,
		Example: `  # Export a specific item by UUID
  icloud-pp-cli photos download --output ~/Desktop 6799AE02-EE45-4469-8AC9-1443582A828E

  # Export a random 10 sensitive videos to a folder (requires --confirm)
  icloud-pp-cli photos download --sensitive --confirm --type video --limit 10 --output ~/Desktop/export

  # Pipe the 5 largest videos into download
  icloud-pp-cli photos top --type video --limit 5 --json \
    | jq -r '.[].uuid' \
    | xargs icloud-pp-cli photos download --output ~/Desktop/big`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !sensitive {
				return usageErr(fmt.Errorf("provide at least one UUID or use --sensitive"))
			}
			if len(args) > 0 && sensitive {
				return usageErr(fmt.Errorf("--sensitive cannot be combined with explicit UUIDs"))
			}
			if mediaType != "all" && mediaType != "photo" && mediaType != "video" {
				return usageErr(fmt.Errorf("--type must be one of: all, photo, video"))
			}
			if sensitive && !confirm {
				return usageErr(fmt.Errorf(
					"--sensitive requires --confirm\n\n" +
						"This flag targets content Apple's on-device ML has flagged as nudity.\n" +
						"Add --confirm to acknowledge and proceed with the export."))
			}

			out := cmd.OutOrStdout()

			// Resolve and create output directory.
			abs, err := filepath.Abs(outputDir)
			if err != nil {
				return fmt.Errorf("invalid output path: %w", err)
			}
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return fmt.Errorf("cannot create output directory: %w", err)
			}

			db, err := openPhotosDB(f.libraryPath)
			if err != nil {
				return err
			}

			var assets []Asset
			if sensitive {
				assets, err = querySensitiveAssets(db, limit, mediaType)
			} else {
				assets, err = queryByUUIDs(db, args)
			}
			db.Close()
			if err != nil {
				return fmt.Errorf("lookup failed: %w", err)
			}

			if len(assets) == 0 {
				fmt.Fprintln(out, "No matching items found.")
				return nil
			}

			fmt.Fprintln(out)
			fmt.Fprintf(out, "Exporting %d item(s) to %s\n\n", len(assets), abs)
			for _, a := range assets {
				fmt.Fprintf(out, "  %s  %s  %s\n",
					yellow(f, out, "→"),
					a.Filename,
					formatSize(f, out, a.SizeGB()),
				)
			}
			fmt.Fprintln(out)
			if batchSize < 1 {
				batchSize = 1
			}
			exported, failed := 0, 0
			for start := 0; start < len(assets); start += batchSize {
				batch := assets[start:min(start+batchSize, len(assets))]
				results := exportBatch(batch, abs)
				for j, a := range batch {
					fmt.Fprintf(out, "  [%d/%d] %s … ", start+j+1, len(assets), a.Filename)
					if r := results[j]; r.Err != nil {
						fmt.Fprintf(out, "%s %v\n", red(f, out, "✗"), r.Err)
						failed++
					} else {
						fmt.Fprintf(out, "%s → %s\n", green(f, out, "✓"), r.Name)
						exported++
					}
				}
			}

			fmt.Fprintln(out)
			fmt.Fprintf(out, "Done — %d exported, %d failed.\n", exported, failed)
			if exported > 0 {
				fmt.Fprintf(out, "Files saved to: %s\n", abs)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output", "o", ".", "Destination folder for exported files")
	cmd.Flags().BoolVar(&sensitive, "sensitive", false, "Export items flagged as containing sensitive content (requires --confirm)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Required when using --sensitive: acknowledge export of nudity-flagged content")
	cmd.Flags().StringVar(&mediaType, "type", "all", "Media type when using --sensitive: all, photo, video")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max items to export when using --sensitive (0 = all)")
	cmd.Flags().IntVar(&batchSize, "batch-size", 10, "Items exported per Photos.app (osascript) session")

	return cmd
}

// exportScript exports each UUID in argv[2:] into its own subfolder
// <argv[1]>/<uuid>/ (created beforehand by the caller). Exporting into an empty
// folder means original-filename collisions (IMG_6963.JPG from different years)
// can't make Photos.app overwrite or rename a file we then fail to detect.
// Prints one "OK\t<uuid>" or "ERR\t<uuid>\t<message>" line per item.
const exportScript = `on run argv
	set rootPath to item 1 of argv
	set results to {}
	repeat with i from 2 to count of argv
		set u to item i of argv
		set destFolder to POSIX file (rootPath & "/" & u)
		try
			set m to my findItem(u)
			tell application "Photos"
				with timeout of 3600 seconds
					export {m} to destFolder using originals true
				end timeout
			end tell
			set end of results to "OK" & tab & u
		on error errMsg
			set end of results to "ERR" & tab & u & tab & errMsg
		end try
	end repeat
	set AppleScript's text item delimiters to linefeed
	return results as text
end run

on findItem(u)
	tell application "Photos"
		-- Direct lookup by local identifier; avoids scanning the whole library.
		try
			set m to media item id (u & "/L0/001")
			get id of m
			return m
		end try
		set found to (media items whose id starts with u)
		if (count of found) is 0 then error "item not found: " & u
		return item 1 of found
	end tell
end findItem`

type exportResult struct {
	Name string // final <UUID>.<ext> filename in destDir
	Err  error
}

var errNotExported = fmt.Errorf("file not found after export — iCloud download may have timed out")

// exportBatch exports assets to destDir in a single osascript session.
// Photos.app downloads originals from iCloud if needed before exporting.
// Results are returned in the same order as assets.
func exportBatch(assets []Asset, destDir string) []exportResult {
	results := make([]exportResult, len(assets))

	workDir, err := os.MkdirTemp(destDir, "icloud-pp-export-")
	if err != nil {
		for i := range results {
			results[i].Err = fmt.Errorf("cannot create temp dir: %w", err)
		}
		return results
	}
	defer os.RemoveAll(workDir)

	args := []string{"-e", exportScript, workDir}
	for i, a := range assets {
		if !uuidRE.MatchString(a.UUID) {
			results[i].Err = fmt.Errorf("invalid UUID %q", a.UUID)
			continue
		}
		if err := os.Mkdir(filepath.Join(workDir, a.UUID), 0o755); err != nil && !os.IsExist(err) {
			results[i].Err = fmt.Errorf("cannot create temp dir: %w", err)
			continue
		}
		args = append(args, a.UUID)
	}
	if len(args) == 3 {
		return results
	}

	var stdout, stderr bytes.Buffer
	c := exec.Command("osascript", args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	status := parseExportOutput(stdout.String())

	// Give the filesystem a moment to flush.
	time.Sleep(200 * time.Millisecond)

	for i, a := range assets {
		if results[i].Err != nil {
			continue
		}
		st, reported := status[a.UUID]
		if reported && st != "" {
			results[i].Err = fmt.Errorf("%s", st)
			continue
		}
		name, err := moveExported(filepath.Join(workDir, a.UUID), destDir, a)
		if err == errNotExported && !reported && runErr != nil {
			// osascript died before reaching this item; surface why.
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = runErr.Error()
			}
			err = fmt.Errorf("%s", msg)
		}
		results[i] = exportResult{Name: name, Err: err}
	}
	return results
}

// parseExportOutput maps UUID → error message ("" for success) from the
// OK/ERR lines printed by exportScript. UUIDs with no line are absent.
func parseExportOutput(s string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		switch {
		case len(parts) >= 2 && parts[0] == "OK":
			m[parts[1]] = ""
		case len(parts) >= 2 && parts[0] == "ERR":
			msg := "export failed"
			if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
				msg = strings.TrimSpace(parts[2])
			}
			m[parts[1]] = msg
		}
	}
	return m
}

// moveExported finds the file Photos.app exported into srcDir and moves it to
// destDir/<UUID>.<lowercased-ext>. Returns the final filename.
func moveExported(srcDir, destDir string, a Asset) (string, error) {
	name, err := pickExported(srcDir, a.Filename)
	if err != nil {
		return "", err
	}
	uuidName := a.UUID + strings.ToLower(filepath.Ext(name))
	if err := os.Rename(filepath.Join(srcDir, name), filepath.Join(destDir, uuidName)); err != nil {
		return "", fmt.Errorf("exported but rename failed: %w", err)
	}
	return uuidName, nil
}

// pickExported returns the exported file in dir, which holds the export of a
// single asset. Sidecars (.aae) and hidden files are ignored. If several files
// remain (e.g. a Live Photo's image + .mov), the one matching want
// (case-insensitive) is chosen.
func pickExported(dir, want string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", errNotExported
	}
	var candidates []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") ||
			strings.EqualFold(filepath.Ext(name), ".aae") {
			continue
		}
		candidates = append(candidates, name)
	}
	switch len(candidates) {
	case 0:
		return "", errNotExported
	case 1:
		return candidates[0], nil
	}
	for _, name := range candidates {
		if strings.EqualFold(name, want) {
			return name, nil
		}
	}
	return "", fmt.Errorf("ambiguous export: %s", strings.Join(candidates, ", "))
}
