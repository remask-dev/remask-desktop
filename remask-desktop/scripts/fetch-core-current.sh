#!/usr/bin/env bash
set -euo pipefail

desktop_dir="$(cd "$(dirname "$0")/.." && pwd)"
target="${TARGET_TRIPLE:-$(rustc -vV | awk '/^host:/ { print $2 }')}"
model_ids="${REMASK_MODEL_IDS:-openai-privacy-filter-q4f16}"
if [[ "${REMASK_RELEASE:-0}" == "1" && "${REMASK_CORE_VERSION:-}" == "latest" ]]; then
  echo "formal desktop releases must pin REMASK_CORE_VERSION or core.version" >&2
  exit 1
fi
mkdir -p "$desktop_dir/src-tauri/binaries" "$desktop_dir/src-tauri/resources/models"
node "$desktop_dir/scripts/fetch-core.mjs" \
  "$desktop_dir/scripts/packaging.lock.json" \
  "$target" \
  "$desktop_dir/src-tauri/binaries" \
  "$desktop_dir/src-tauri/resources/models" \
  "$model_ids"
