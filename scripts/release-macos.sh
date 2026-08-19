#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
desktop_dir="$(cd "$script_dir/.." && pwd)"
workspace_dir="$(cd "$desktop_dir/.." && pwd)"
tauri_dir="$desktop_dir/src-tauri"
keychain_service="${REMASK_NOTARY_KEYCHAIN_SERVICE:-remask-notary-password}"

log() {
  printf '[release-macos] %s\n' "$*"
}

die() {
  printf '[release-macos] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_value() {
  local name="$1"
  [[ -n "${!name:-}" ]] || die "$name is required"
}

require_command git
require_command hdiutil
require_command node
require_command npm
require_command security
require_command shasum
require_command spctl
require_command xcrun

[[ "$(uname -s)" == "Darwin" ]] || die "formal macOS releases must be built on macOS"

package_version="$(node -p "require('$desktop_dir/package.json').version")"
tauri_version="$(node -p "require('$tauri_dir/tauri.conf.json').version")"
cargo_version="$(awk -F ' *= *' '/^version *=/ { gsub(/\"/, "", $2); print $2; exit }' "$tauri_dir/Cargo.toml")"
[[ "$package_version" == "$tauri_version" && "$package_version" == "$cargo_version" ]] || \
  die "release versions differ: package=$package_version tauri=$tauri_version cargo=$cargo_version"

cd "$workspace_dir"
commit="$(git rev-parse --verify HEAD)"
expected_tag="v$package_version"

if [[ "${REMASK_ALLOW_DIRTY:-0}" != "1" ]]; then
  worktree_status="$(git status --porcelain --untracked-files=normal)"
  [[ -z "$worktree_status" ]] || {
    printf '%s\n' "$worktree_status" >&2
    die "worktree must be clean; use REMASK_ALLOW_DIRTY=1 only for a release-candidate test"
  }
fi

if [[ "${REMASK_ALLOW_UNTAGGED:-0}" != "1" ]]; then
  [[ "$(git tag --points-at HEAD --list "$expected_tag")" == "$expected_tag" ]] || \
    die "HEAD must have the release tag $expected_tag; use REMASK_ALLOW_UNTAGGED=1 only for a release-candidate test"
fi

if [[ -z "${APPLE_SIGNING_IDENTITY:-}" ]]; then
  identities=()
  while IFS= read -r identity; do
    [[ -n "$identity" ]] && identities+=("$identity")
  done < <(
    security find-identity -v -p codesigning | \
      sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p'
  )
  if ((${#identities[@]} == 1)); then
    APPLE_SIGNING_IDENTITY="${identities[0]}"
    export APPLE_SIGNING_IDENTITY
  else
    security find-identity -v -p codesigning >&2
    die "set APPLE_SIGNING_IDENTITY to exactly one Developer ID Application identity"
  fi
fi
security find-identity -v -p codesigning | grep -F "$APPLE_SIGNING_IDENTITY" >/dev/null || \
  die "APPLE_SIGNING_IDENTITY is not available in the keychain"

if [[ -n "${APPLE_API_ISSUER:-}" || -n "${APPLE_API_KEY:-}" || -n "${APPLE_API_KEY_PATH:-}" ]]; then
  require_value APPLE_API_ISSUER
  require_value APPLE_API_KEY
  require_value APPLE_API_KEY_PATH
  [[ -f "$APPLE_API_KEY_PATH" ]] || die "Apple notarization key not found: $APPLE_API_KEY_PATH"
else
  require_value APPLE_ID
  require_value APPLE_TEAM_ID
  if [[ -z "${APPLE_PASSWORD:-}" ]]; then
    APPLE_PASSWORD="$(security find-generic-password -a "$APPLE_ID" -s "$keychain_service" -w 2>/dev/null)" || \
      die "notarization password was not found in Keychain service $keychain_service"
    export APPLE_PASSWORD
  fi
fi

log "version=$package_version commit=$commit"
log "signing identity=$APPLE_SIGNING_IDENTITY"

cd "$desktop_dir"
if [[ "${REMASK_SKIP_NPM_CI:-0}" != "1" ]]; then
  log "installing locked desktop dependencies"
  npm ci
fi

log "building, signing, and notarizing macOS release"
REMASK_RELEASE=1 REMASK_SIGN=1 bash "$script_dir/package.sh" macos

case "$(uname -m)" in
  arm64|aarch64) release_arch="arm64" ;;
  x86_64|amd64) release_arch="x64" ;;
  *) die "unsupported macOS architecture: $(uname -m)" ;;
esac

release_dir="$workspace_dir/artifacts/macos/$release_arch/$package_version"
[[ -f "$release_dir/SHA256SUMS" ]] || die "release checksums were not generated"

cd "$release_dir"
shasum -a 256 -c SHA256SUMS

dmg_files=()
while IFS= read -r -d '' dmg; do dmg_files+=("$dmg"); done < <(
  find "$release_dir" -maxdepth 1 -type f -name '*.dmg' -print0
)
((${#dmg_files[@]} == 1)) || die "expected exactly one DMG in $release_dir"

hdiutil verify "${dmg_files[0]}"
spctl --assess --type open --context context:primary-signature --verbose=2 "${dmg_files[0]}"
xcrun stapler validate "${dmg_files[0]}"

log "release ready: ${dmg_files[0]}"
log "checksums: $release_dir/SHA256SUMS"
