#!/bin/sh
# icloudcli installer
# https://icloudcli.com
#
# Usage:
#   curl -fsSL https://icloudcli.com/install.sh | sh

set -e

REPO="github.com/matysanchez/icloudcli"
BIN="icloud-pp-cli"

# ── checks ──────────────────────────────────────────────────────────

if [ "$(uname -s)" != "Darwin" ]; then
  printf '\033[31merror:\033[0m icloudcli only runs on macOS.\n' >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  printf '\033[31merror:\033[0m Go is required but was not found.\n' >&2
  printf '       Install Go from https://go.dev/dl/ then re-run:\n' >&2
  printf '\n         curl -fsSL https://icloudcli.com/install.sh | sh\n\n' >&2
  exit 1
fi

# ── install ─────────────────────────────────────────────────────────

printf 'Installing %s...\n' "$BIN"
go install "${REPO}/cmd/${BIN}@latest"

# ── verify ──────────────────────────────────────────────────────────

if command -v "$BIN" >/dev/null 2>&1; then
  printf '\033[32m✓\033[0m %s installed at %s\n' "$BIN" "$(command -v "$BIN")"
  printf '\nRun \033[36m%s doctor\033[0m to verify your setup.\n\n' "$BIN"
else
  printf '\n\033[33mwarning:\033[0m %s installed but not found in PATH.\n' "$BIN" >&2
  printf 'Add Go'\''s bin directory to your PATH and restart your terminal:\n\n' >&2
  printf '  echo '\''export PATH="$PATH:$(go env GOPATH)/bin"'\'' >> ~/.zshrc\n' >&2
  printf '  source ~/.zshrc\n\n' >&2
fi
