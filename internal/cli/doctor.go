// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// PATCH(doctor-sectioned-output): emits System/Library/Assets/Messages sections for structured pre-flight.
// PATCH(messages-fda-doctor-check): the Messages section classifies EPERM (and modernc.org/sqlite's
// SQLITE_CANTOPEN surrogate) on chat.db open as a Full Disk Access denial with copy-paste remediation.
func newDoctorCmd(f *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check system requirements and access to every data source",
		Long: `Run pre-flight checks before using any other command.

Verifies: macOS, Photos.app + library schema, Messages and Safari Full Disk
Access, and the Automation permission the Notes/Reminders/Calendar groups
need. Each section degrades to a warning so one unavailable source never
blocks the rest.`,
		Example: `  icloud-pp-cli doctor
  icloud-pp-cli doctor --library "/Volumes/External/Photos Library.photoslibrary/database/Photos.sqlite"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			allOK := true

			check := func(label, hint string, ok bool) {
				allOK = allOK && ok
				if ok {
					fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), label)
				} else {
					fmt.Fprintf(out, "  %s %s\n", red(f, out, "✗"), label)
					if hint != "" {
						fmt.Fprintf(out, "      %s\n", hint)
					}
				}
			}

			// warn prints a yellow ⚠ without affecting allOK — used for optional
			// features that are unavailable on this library but don't block core commands.
			warn := func(label, hint string) {
				fmt.Fprintf(out, "  %s %s\n", yellow(f, out, "⚠"), label)
				if hint != "" {
					fmt.Fprintf(out, "      %s\n", hint)
				}
			}

			// ── System ────────────────────────────────────────────────
			fmt.Fprintln(out, bold(f, out, "System"))

			isDarwin := runtime.GOOS == "darwin"
			check("macOS required", "icloud-pp-cli only runs on macOS.", isDarwin)
			if !isDarwin {
				fmt.Fprintln(out)
				fmt.Fprintln(out, red(f, out, "Stopped — macOS required."))
				return nil
			}

			ver := macOSVersion()
			fmt.Fprintf(out, "      macOS %s\n", ver)

			photosPath := photosAppPath()
			check("Photos.app installed",
				"Photos.app not found in /System/Applications or /Applications.", photosPath != "")

			// ── Library ───────────────────────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Library"))

			libPath := f.libraryPath
			if libPath == "" {
				libPath = defaultLibraryPath()
			}

			_, statErr := os.Stat(libPath)
			check("Library found", fmt.Sprintf(
				"Not found at:\n      %s\n      Use --library to specify a custom path.", libPath,
			), statErr == nil)

			if statErr != nil {
				fmt.Fprintln(out)
				fmt.Fprintln(out, red(f, out, "Stopped — library not found."))
				return nil
			}
			fmt.Fprintf(out, "      %s\n", libPath)

			db, openErr := openPhotosDB(libPath)
			check("Library readable (read-only)",
				"Try quitting Photos.app and running again.", openErr == nil)

			if openErr != nil {
				fmt.Fprintln(out)
				fmt.Fprintln(out, red(f, out, "Stopped — cannot read library."))
				return nil
			}
			defer db.Close()

			schemaOK := checkCoreSchema(db)
			check("Core schema valid (ZASSET + ZADDITIONALASSETATTRIBUTES)",
				"Unexpected schema — may be an unsupported Photos version.", schemaOK)

			// Optional: ML sensitivity column — only needed by photos download --sensitive.
			// Absent on macOS 12 or earlier; does not block core commands.
			if checkSensitiveColumn(db) {
				fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "ML analysis column present (photos download --sensitive available)")
			} else {
				warn("ML analysis column absent — photos download --sensitive unavailable",
					"Column ZSCREENTIMEDEVICEIMAGESENSITIVITY not found; requires macOS 13 or later.")
			}

			// ── Assets ────────────────────────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Assets"))

			count, sizeBytes, countErr := queryTotals(db)
			check("Can query assets", "Unexpected query error.", countErr == nil)

			if countErr == nil {
				gb := float64(sizeBytes) / (1 << 30)
				fmt.Fprintf(out, "      %s items · %.2f GB (original sizes)\n",
					formatInt(count), gb)

				byType, _ := queryStorageByType(db)
				for _, r := range byType {
					fmt.Fprintf(out, "      %-12s %s items\n",
						r.Label+":", formatInt(r.Count))
				}
			}

			// ── Messages ──────────────────────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Messages"))

			msgPath := f.messagesDBPath
			if msgPath == "" {
				msgPath = defaultMessagesDBPath()
			}

			_, msgStatErr := os.Stat(msgPath)
			if msgStatErr != nil {
				warn("chat.db not present — messages commands unavailable",
					fmt.Sprintf("Open Messages.app once to initialize the database, or use --messages-db.\n      Expected at: %s", msgPath))
			} else {
				fmt.Fprintf(out, "      %s\n", msgPath)
				msgDB, msgOpenErr := openMessagesDB(msgPath)
				switch {
				case msgOpenErr == nil:
					fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "chat.db readable (Full Disk Access granted)")
					msgSchemaOK := checkMessagesSchema(msgDB)
					if msgSchemaOK {
						fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "Messages schema valid (message + chat + handle)")
						msgTotals, totalsErr := statsTotals(msgDB, false)
						if totalsErr == nil {
							fmt.Fprintf(out, "      %s messages · %s chats · %s handles\n",
								formatInt(msgTotals.TotalMessages),
								formatInt(msgTotals.TotalChats),
								formatInt(msgTotals.TotalHandles))
						}
					} else {
						warn("Messages schema unexpected", "")
					}
					msgDB.Close()
				case errors.Is(msgOpenErr, errFDADenied):
					warn("Full Disk Access not granted — messages commands unavailable",
						"Open System Settings > Privacy & Security > Full Disk Access,\n      add your terminal app, quit and reopen the terminal, then rerun doctor.")
				default:
					warn("chat.db cannot be opened",
						msgOpenErr.Error())
				}
			}

			// ── Safari ────────────────────────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Safari"))

			safariPath := defaultSafariHistoryPath()
			if _, err := os.Stat(safariPath); err != nil {
				warn("History.db not present — safari history unavailable",
					fmt.Sprintf("Expected at: %s", safariPath))
			} else {
				fmt.Fprintf(out, "      %s\n", safariPath)
				sdb, sErr := openSafariHistory(safariPath)
				switch {
				case sErr == nil:
					fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "History.db readable (Full Disk Access granted)")
					if ov, ovErr := safariOverview(sdb); ovErr == nil {
						fmt.Fprintf(out, "      %s URLs · %s visits · %s domains\n",
							formatInt(ov.TotalURLs), formatInt(ov.TotalVisits), formatInt(ov.DistinctDoms))
					}
					sdb.Close()
				case errors.Is(sErr, errFDADenied):
					warn("Full Disk Access not granted — safari history unavailable",
						"Open System Settings > Privacy & Security > Full Disk Access,\n      add your terminal app, quit and reopen the terminal, then rerun doctor.")
				default:
					warn("History.db cannot be opened", sErr.Error())
				}
			}

			// ── Call History + Screen Time (Full Disk Access) ─────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Call History · Screen Time"))
			fdaProbe := func(label, path string, open func(string) (*sql.DB, error)) {
				if _, err := os.Stat(path); err != nil {
					warn(label+" not present", fmt.Sprintf("Expected at: %s", path))
					return
				}
				db, oErr := open(path)
				switch {
				case oErr == nil:
					fmt.Fprintf(out, "  %s %s readable (Full Disk Access granted)\n", green(f, out, "✓"), label)
					db.Close()
				case errors.Is(oErr, errFDADenied):
					warn("Full Disk Access not granted — "+label+" unavailable",
						"Add your terminal under System Settings > Privacy & Security > Full Disk Access, then reopen it.")
				default:
					warn(label+" cannot be opened", oErr.Error())
				}
			}
			fdaProbe("Call History", defaultCallHistoryPath(), openCallHistory)
			fdaProbe("Screen Time", defaultKnowledgeDBPath(), openKnowledgeDB)
			fdaProbe("Mail", defaultMailIndexPath(), openMailIndex)

			// ── No-permission data sources ────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "iCloud Drive · Podcasts"))
			if _, err := os.Stat(mobileDocumentsPath()); err != nil {
				warn("iCloud Drive not found — is iCloud Drive enabled?",
					fmt.Sprintf("Expected at: %s", mobileDocumentsPath()))
			} else {
				fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "iCloud Drive readable (no extra permission needed)")
			}
			if _, err := os.Stat(defaultPodcastsDBPath()); err != nil {
				warn("Podcasts library not present", "Open Apple Podcasts once to create it.")
			} else {
				fmt.Fprintf(out, "  %s %s\n", green(f, out, "✓"), "Podcasts library readable (no extra permission needed)")
			}

			// ── Automation note ───────────────────────────────────────
			fmt.Fprintln(out)
			fmt.Fprintln(out, bold(f, out, "Automation (Notes · Reminders · Calendar · Music)"))
			fmt.Fprintf(out, "  %s %s\n", yellow(f, out, "i"),
				"These groups read scriptable apps via Automation; macOS prompts on first sync.")
			fmt.Fprintln(out, "      Approve the prompt, or pre-grant under System Settings >")
			fmt.Fprintln(out, "      Privacy & Security > Automation > your terminal.")

			// ── Result ────────────────────────────────────────────────
			fmt.Fprintln(out)
			if allOK {
				fmt.Fprintln(out, green(f, out, "All checks passed. Ready to use."))
			} else {
				fmt.Fprintln(out, yellow(f, out, "Some checks failed — see above."))
			}

			return nil
		},
	}

	return cmd
}

// checkMessagesSchema verifies the tables an iMessage query touches.
//
// PATCH(messages-doctor-junction-tables): every messages query joins
// chat_message_join, and `messages export` additionally touches
// message_attachment_join. A stripped or very old chat.db can be missing
// these without missing the primary tables, which used to make doctor
// report "schema valid" but the first real query fail.
func checkMessagesSchema(db *sql.DB) bool {
	tables := []string{
		"message",
		"chat",
		"handle",
		"chat_message_join",
		"message_attachment_join",
	}
	for _, table := range tables {
		var name string
		if err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name); err != nil || name != table {
			return false
		}
	}
	return true
}

func photosAppPath() string {
	for _, p := range []string{
		"/System/Applications/Photos.app",
		"/Applications/Photos.app",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func macOSVersion() string {
	b, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

// checkCoreSchema verifies the two tables required by every command.
func checkCoreSchema(db *sql.DB) bool {
	for _, table := range []string{"ZASSET", "ZADDITIONALASSETATTRIBUTES"} {
		var name string
		if err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name); err != nil || name != table {
			return false
		}
	}
	return true
}

// checkSensitiveColumn verifies that ZMEDIAANALYSISASSETATTRIBUTES exists and
// contains the ZSCREENTIMEDEVICEIMAGESENSITIVITY column used by --sensitive.
// This is optional: absent on macOS 12 or earlier; only blocks photos download --sensitive.
func checkSensitiveColumn(db *sql.DB) bool {
	var name string
	if err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='ZMEDIAANALYSISASSETATTRIBUTES'",
	).Scan(&name); err != nil {
		return false
	}
	var colCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('ZMEDIAANALYSISASSETATTRIBUTES') WHERE name='ZSCREENTIMEDEVICEIMAGESENSITIVITY'",
	).Scan(&colCount); err != nil || colCount == 0 {
		return false
	}
	return true
}

func formatFloat(f float64) string {
	return fmt.Sprintf("%.2f", f)
}

func formatInt(n int64) string {
	if n < 0 {
		return "-" + formatInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	out := []byte{}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
