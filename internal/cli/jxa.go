// Copyright 2026 matysanchez. Licensed under Apache-2.0. See LICENSE.

// Package cli — jxa.go
// Shared JavaScript-for-Automation (JXA) helpers used by the command groups
// that read iCloud-synced data out of scriptable macOS apps (Notes, Reminders,
// Calendar). All of these talk to an app via `osascript -l JavaScript` and need
// the same launch-on-demand retry behavior, so the logic lives here once.
package cli

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runJXAForApp runs a JXA script that targets a scriptable macOS app. If the
// first attempt fails because the app is not running (Apple event error -600 or
// "isn't running"), the app is launched with `open -a` and the script retried a
// few times while the scripting bridge initializes. This mirrors the retry the
// Contacts commands use, generalized over the app name so Notes/Reminders/
// Calendar can share it.
func runJXAForApp(appName, script string) (string, error) {
	out, err := rawJXA(script)
	if err == nil {
		return out, nil
	}
	if strings.Contains(err.Error(), "-600") || strings.Contains(err.Error(), "isn't running") {
		if launchErr := exec.Command("open", "-g", "-a", appName).Run(); launchErr == nil {
			for i := 0; i < 8; i++ {
				time.Sleep(500 * time.Millisecond)
				if out2, err2 := rawJXA(script); err2 == nil {
					return out2, nil
				}
			}
		}
	}
	return "", err
}

// rawJXA executes a JavaScript-for-Automation script once and returns trimmed
// stdout. Errors include the script's stderr (which carries the Apple event
// error detail) so callers can classify automation-permission denials.
func rawJXA(script string) (string, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runJXAForAppArgs is runJXAForApp for scripts that take parameters. The script
// must define `function run(argv) { ... }`; the args are passed after `--` so
// osascript forwards them to run()'s argv. Values are never interpolated into
// the script body, so they cannot break out of the JS string context.
func runJXAForAppArgs(appName, script string, args ...string) (string, error) {
	out, err := rawJXAArgs(script, args...)
	if err == nil {
		return out, nil
	}
	if strings.Contains(err.Error(), "-600") || strings.Contains(err.Error(), "isn't running") {
		if launchErr := exec.Command("open", "-g", "-a", appName).Run(); launchErr == nil {
			for i := 0; i < 8; i++ {
				time.Sleep(500 * time.Millisecond)
				if out2, err2 := rawJXAArgs(script, args...); err2 == nil {
					return out2, nil
				}
			}
		}
	}
	return "", err
}

func rawJXAArgs(script string, args ...string) (string, error) {
	cmdArgs := append([]string{"-l", "JavaScript", "-e", script, "--"}, args...)
	cmd := exec.Command("osascript", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isAutomationDenied reports whether an osascript error looks like a macOS
// Automation (TCC) permission denial — error -1743 ("Not authorized to send
// Apple events") or the textual form. Used to surface a clear remediation
// message instead of a raw osascript error.
func isAutomationDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "-1743") ||
		strings.Contains(msg, "Not authorized to send Apple events") ||
		strings.Contains(msg, "not allowed assistive access")
}

// automationHint returns a one-line remediation string for an Automation denial,
// naming the app the user needs to authorize.
func automationHint(appName string) string {
	return fmt.Sprintf("Grant Automation access: System Settings > Privacy & Security > Automation > "+
		"your terminal > enable %q, then retry.", appName)
}
