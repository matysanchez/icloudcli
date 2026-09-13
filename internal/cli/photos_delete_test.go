// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

package cli

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPhotosDeleteScriptUsesUUIDPrefix(t *testing.T) {
	const uuid = "C9E82681-2B33-4555-AC95-5F2601F561F1"

	if strings.Contains(photosDeleteScript, uuid) {
		t.Fatal("delete script must receive the UUID through argv, not interpolation")
	}
	if !strings.Contains(photosDeleteScript, "id starts with targetUUID") {
		t.Fatal("delete script must match Photos IDs by UUID prefix")
	}
	if !strings.Contains(photosDeleteScript, "(count of theItems) is not 1") {
		t.Fatal("delete script must reject ambiguous UUID prefix matches")
	}
}

func TestPhotosDeleteScriptCompiles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Photos AppleScript is macOS-only")
	}

	out, err := exec.Command(
		"osacompile",
		"-e", photosDeleteScript,
		"-o", filepath.Join(t.TempDir(), "photos-delete.scpt"),
	).CombinedOutput()
	if err != nil {
		t.Fatalf("compile delete script: %v: %s", err, out)
	}
}
