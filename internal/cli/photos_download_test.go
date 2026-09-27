// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPickExported(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		want    string
		pick    string
		wantErr bool
	}{
		{"single file", []string{"IMG_6963.JPG"}, "IMG_6963.JPG", "IMG_6963.JPG", false},
		{"renamed by Photos", []string{"IMG_6963 (1).jpg"}, "IMG_6963.JPG", "IMG_6963 (1).jpg", false},
		{"ignores aae and hidden", []string{"IMG_1.HEIC", "IMG_1.AAE", ".DS_Store"}, "IMG_1.HEIC", "IMG_1.HEIC", false},
		{"live photo picks match", []string{"IMG_2.HEIC", "IMG_2.MOV"}, "img_2.heic", "IMG_2.HEIC", false},
		{"ambiguous", []string{"a.jpg", "b.jpg"}, "c.jpg", "", true},
		{"empty", nil, "x.jpg", "", true},
		{"only sidecar", []string{"x.aae"}, "x.jpg", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, c.files...)
			got, err := pickExported(dir, c.want)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.pick {
				t.Errorf("got %q, want %q", got, c.pick)
			}
		})
	}
}

func TestPickExportedMissingDir(t *testing.T) {
	if _, err := pickExported(filepath.Join(t.TempDir(), "nope"), "x.jpg"); err != errNotExported {
		t.Errorf("err = %v, want errNotExported", err)
	}
}

// Two assets sharing an original filename must both land as <uuid>.<ext>.
func TestMoveExportedFilenameCollision(t *testing.T) {
	out := t.TempDir()
	work := t.TempDir()
	assets := []Asset{
		{UUID: "11111111-1111-1111-1111-111111111111", Filename: "IMG_6963.JPG"},
		{UUID: "22222222-2222-2222-2222-222222222222", Filename: "IMG_6963.JPG"},
	}
	// Pre-existing file in the output dir with the same original name.
	writeFiles(t, out, "IMG_6963.JPG")
	for _, a := range assets {
		sub := filepath.Join(work, a.UUID)
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, sub, "IMG_6963.JPG", "IMG_6963.AAE")
		name, err := moveExported(sub, out, a)
		if err != nil {
			t.Fatalf("%s: %v", a.UUID, err)
		}
		if want := a.UUID + ".jpg"; name != want {
			t.Errorf("name = %q, want %q", name, want)
		}
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing output: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "IMG_6963.JPG")); err != nil {
		t.Errorf("pre-existing file touched: %v", err)
	}
}

func TestParseExportOutput(t *testing.T) {
	in := "OK\tAAA\nERR\tBBB\tPhotos got an error: timed out\r\nERR\tCCC\t\ngarbage\n"
	got := parseExportOutput(in)
	if msg, ok := got["AAA"]; !ok || msg != "" {
		t.Errorf("AAA = %q, %v", msg, ok)
	}
	if got["BBB"] != "Photos got an error: timed out" {
		t.Errorf("BBB = %q", got["BBB"])
	}
	if got["CCC"] != "export failed" {
		t.Errorf("CCC = %q", got["CCC"])
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}
