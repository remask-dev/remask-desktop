#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "$0")/.." && pwd)"
tauri_dir="$project_dir/src-tauri"
icons_dir="$tauri_dir/icons"

cd "$project_dir"

# Every platform is generated directly from the one approved master icon.
npx tauri icon "$tauri_dir/app-icon.svg" --output "$icons_dir"

echo "Generated platform-specific icons in $icons_dir"
