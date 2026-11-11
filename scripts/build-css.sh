#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Tailwind CSS Standalone CLI Builder (Zero Node.js / Zero npm)
# Automatically downloads the official Tailwind binary for Linux and compiles
# backend/internal/web/static/input.css -> backend/internal/web/static/tailwind.css
# ==============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
TOOLS_DIR="$ROOT_DIR/tools"
TAILWIND_BIN="$TOOLS_DIR/tailwindcss"

mkdir -p "$TOOLS_DIR"

if [ ! -f "$TAILWIND_BIN" ]; then
    echo "[+] Lade offizielle Tailwind CSS Standalone CLI herunter..."
    ARCH="x64"
    if [ "$(uname -m)" = "aarch64" ]; then
        ARCH="arm64"
    fi
    curl -sLo "$TAILWIND_BIN" "https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-linux-${ARCH}"
    chmod +x "$TAILWIND_BIN"
fi

WATCH_FLAG=""
if [ "${1:-}" = "--watch" ] || [ "${1:-}" = "-w" ]; then
    WATCH_FLAG="--watch"
    echo "[+] Starte Tailwind CSS Watcher..."
else
    echo "[+] Kompiliere Tailwind CSS..."
fi

"$TAILWIND_BIN" -i "$ROOT_DIR/backend/internal/web/static/input.css" -o "$ROOT_DIR/backend/internal/web/static/tailwind.css" --minify $WATCH_FLAG
cp -f "$ROOT_DIR/backend/internal/web/static/tailwind.css" "$ROOT_DIR/backend/static/tailwind.css"
cp -f "$ROOT_DIR/backend/internal/web/static/input.css" "$ROOT_DIR/backend/static/input.css"

echo "[✔] Tailwind CSS erfolgreich kompiliert und synchronisiert!"
