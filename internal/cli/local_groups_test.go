// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"testing"
	"time"
)

func TestExtractNoteUUID(t *testing.T) {
	cases := map[string]string{
		"x-coredata://ABC-123/ICNote/p456": "p456",
		"x-coredata://store/ICReminder/p9": "p9",
		"noslash":                          "noslash",
		"":                                 "",
		"trailing/":                        "trailing/", // trailing slash → fall back to full
	}
	for in, want := range cases {
		if got := extractNoteUUID(in); got != want {
			t.Errorf("extractNoteUUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMakeSnippet(t *testing.T) {
	if got := makeSnippet("hello   world\n\nfoo", 100); got != "hello world foo" {
		t.Errorf("collapse whitespace: got %q", got)
	}
	got := makeSnippet("abcdefghij", 5)
	if got != "abcde…" {
		t.Errorf("truncation: got %q, want %q", got, "abcde…")
	}
	// Multi-byte runes must not be split mid-rune.
	emoji := makeSnippet("😀😀😀😀😀", 2)
	if []rune(emoji)[0] != '😀' || len([]rune(emoji)) != 3 { // 2 runes + ellipsis
		t.Errorf("emoji truncation: got %q (%d runes)", emoji, len([]rune(emoji)))
	}
}

func TestPriorityLabel(t *testing.T) {
	cases := map[int]string{0: "", 1: "high", 4: "high", 5: "medium", 9: "low", 7: "low"}
	for in, want := range cases {
		if got := priorityLabel(in); got != want {
			t.Errorf("priorityLabel(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Error("boolToInt mapping wrong")
	}
}

func TestDurationMinutes(t *testing.T) {
	start := "2026-06-08T09:00:00Z"
	end := "2026-06-08T10:30:00Z"
	if got := durationMinutes(start, end); got != 90 {
		t.Errorf("durationMinutes = %d, want 90", got)
	}
	// Inverted range and unparseable inputs clamp to 0.
	if got := durationMinutes(end, start); got != 0 {
		t.Errorf("inverted range = %d, want 0", got)
	}
	if got := durationMinutes("bad", end); got != 0 {
		t.Errorf("unparseable = %d, want 0", got)
	}
}

func TestParseDateRangeFlag(t *testing.T) {
	// Empty flag uses fallback.
	if got, _ := parseDateRangeFlag("", "2026-01-01T00:00:00Z"); got != "2026-01-01T00:00:00Z" {
		t.Errorf("fallback not used: %q", got)
	}
	// Valid date parses to RFC3339 (local) — check the date prefix survives.
	got, err := parseDateRangeFlag("2026-03-15", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[:10] != "2026-03-15" {
		t.Errorf("parsed date prefix = %q, want 2026-03-15", got[:10])
	}
	// Bad format errors.
	if _, err := parseDateRangeFlag("03/15/2026", ""); err == nil {
		t.Error("expected error for bad date format")
	}
}

func TestShortHelpers(t *testing.T) {
	if shortID("0123456789abcdef") != "01234567" {
		t.Error("shortID should take first 8")
	}
	if shortID("") != "-" {
		t.Error("shortID empty → -")
	}
	if shortID("abc") != "abc" {
		t.Error("shortID short string unchanged")
	}
	if shortDate("") != "-" {
		t.Error("shortDate empty → -")
	}
	if got := shortDate("2026-06-08T12:34:56Z"); got[:4] != "2026" {
		t.Errorf("shortDate year = %q", got)
	}
	if joinArgs([]string{"foo", "bar"}) != "foo bar" {
		t.Error("joinArgs should space-join")
	}
}

func TestBarOfWidth(t *testing.T) {
	if got := barOfWidth(3, 5); len([]rune(got)) != 5 {
		t.Errorf("bar width = %d runes, want 5", len([]rune(got)))
	}
	// Clamps out-of-range n.
	if got := barOfWidth(99, 4); len([]rune(got)) != 4 {
		t.Errorf("overflow bar = %d runes, want 4", len([]rune(got)))
	}
	if got := barOfWidth(-5, 3); len([]rune(got)) != 3 {
		t.Errorf("negative bar = %d runes, want 3", len([]rune(got)))
	}
}

func TestFlattenBookmarks(t *testing.T) {
	root := plistNode{
		WebBookmarkType: "WebBookmarkTypeList",
		Children: []plistNode{
			{
				WebBookmarkType: "WebBookmarkTypeLeaf",
				URLString:       "https://a.com",
				URIDictionary:   map[string]interface{}{"title": "A"},
			},
			{
				WebBookmarkType: "WebBookmarkTypeList",
				Title:           "Work",
				Children: []plistNode{
					{
						WebBookmarkType: "WebBookmarkTypeLeaf",
						URLString:       "https://b.com",
						URIDictionary:   map[string]interface{}{"title": "B"},
					},
				},
			},
		},
	}
	var out []Bookmark
	flattenBookmarks(root, "", &out)
	if len(out) != 2 {
		t.Fatalf("expected 2 bookmarks, got %d", len(out))
	}
	if out[0].Title != "A" || out[0].Folder != "" {
		t.Errorf("top-level leaf wrong: %+v", out[0])
	}
	if out[1].Title != "B" || out[1].Folder != "Work" {
		t.Errorf("nested leaf wrong: %+v", out[1])
	}
}

func TestSafariEpochConversion(t *testing.T) {
	// safariEpoch added to a Cocoa timestamp of 0 yields 2001-01-01 UTC.
	got := time.Unix(0+safariEpoch, 0).UTC()
	if got.Year() != 2001 || got.Month() != time.January || got.Day() != 1 {
		t.Errorf("safariEpoch base = %v, want 2001-01-01", got)
	}
}

func TestCocoaSeconds(t *testing.T) {
	// cocoaSeconds is the inverse offset used by the calendar activity query.
	ref := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := cocoaSeconds(ref); got != 0 {
		t.Errorf("cocoaSeconds(2001-01-01) = %d, want 0", got)
	}
}

func TestFriendlyContainer(t *testing.T) {
	cases := map[string]string{
		"com~apple~CloudDocs":              "iCloud Drive",
		"57T9237FN3~net~whatsapp~WhatsApp": "WhatsApp",
		"iCloud~md~obsidian":               "obsidian",
		"com~apple~Pages":                  "Pages",
	}
	for in, want := range cases {
		if got := friendlyContainer(in); got != want {
			t.Errorf("friendlyContainer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsTeamID(t *testing.T) {
	if !isTeamID("57T9237FN3") {
		t.Error("valid team ID rejected")
	}
	if isTeamID("apple") || isTeamID("57t9237fn3") || isTeamID("57T9237FN3X") {
		t.Error("invalid team ID accepted (lowercase / wrong length)")
	}
}

func TestCloudLogicalName(t *testing.T) {
	if got := cloudLogicalName(".Report.pdf.icloud"); got != "Report.pdf" {
		t.Errorf("cloudLogicalName placeholder = %q, want Report.pdf", got)
	}
	if got := cloudLogicalName("Report.pdf"); got != "Report.pdf" {
		t.Errorf("cloudLogicalName passthrough = %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		1073741824: "1.0 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCallServiceLabel(t *testing.T) {
	cases := map[string]string{
		"com.apple.FaceTimeVideo": "facetime-video",
		"com.apple.FaceTimeAudio": "facetime-audio",
		"com.apple.Telephony":     "phone",
		"":                        "",
		"com.example.Unknown":     "com.example.Unknown",
	}
	for in, want := range cases {
		if got := callServiceLabel(in); got != want {
			t.Errorf("callServiceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int64]string{
		0:    "—",
		45:   "45s",
		90:   "1m 30s",
		3661: "1h 1m",
	}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}
