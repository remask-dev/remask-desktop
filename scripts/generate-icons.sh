#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "$0")/.." && pwd)"
tauri_dir="$project_dir/src-tauri"
icons_dir="$tauri_dir/icons"

cd "$project_dir"

# App icons for each platform are generated from the approved master icon.
npx tauri icon "$tauri_dir/app-icon.svg" --output "$icons_dir"

# The menu-bar icon is a template image: its alpha channel carries the
# intentional strong/weak hierarchy and must stay monochrome on macOS.
if command -v magick >/dev/null 2>&1; then
  image_tool=(magick)
elif command -v convert >/dev/null 2>&1; then
  image_tool=(convert)
else
  echo "ImageMagick (magick or convert) is required to render tray-icon.svg" >&2
  exit 1
fi
"${image_tool[@]}" -background none -density 384 "$tauri_dir/tray-icon.svg" \
  -resize 64x64 -define png:color-type=6 "$icons_dir/tray-icon.png"

echo "Generated platform-specific icons in $icons_dir"
