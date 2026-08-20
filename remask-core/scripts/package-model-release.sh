#!/usr/bin/env bash
set -euo pipefail

model_root="${1:-models}"
model_id="${2:-openai-privacy-filter-q4f16}"
version="${3:-${REMASK_MODEL_VERSION:-}}"
output="${4:-dist}"
model_dir="$model_root/$model_id"

die() { printf '[core-release] error: %s\n' "$*" >&2; exit 1; }
sha256_file() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }
[[ "$model_id" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid model id"
[[ -n "$version" ]] || die "model version is required"
[[ -f "$model_dir/manifest.json" ]] || die "model manifest not found: $model_dir/manifest.json"
mkdir -p "$output"
manifest_sha="$(sha256_file "$model_dir/manifest.json")"
asset="${model_id}-v${version}.tar.gz"
tar -czf "$output/$asset" -C "$model_root" "$model_id"
asset_sha="$(sha256_file "$output/$asset")"
cat > "$output/model-${model_id}.json" <<EOF
{
  "modelID": "$model_id",
  "asset": "$asset",
  "sha256": "$asset_sha",
  "manifest": "$model_id/manifest.json",
  "manifestSha256": "$manifest_sha"
}
EOF
printf '%s  %s\n' "$asset_sha" "$asset" >> "$output/SHA256SUMS"
printf '[core-release] wrote %s\n' "$output/$asset"
