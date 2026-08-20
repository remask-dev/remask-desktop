#!/usr/bin/env bash
set -euo pipefail

# Build one immutable Core runtime asset. The private Core repository runs this
# script in its protected release workflow; the desktop repository only sees
# the resulting archive and manifest published as a public Release asset.

script_dir="$(cd "$(dirname "$0")" && pwd)"
core_dir="$(cd "$script_dir/.." && pwd)"
target="${1:-$(go env GOOS)-$(go env GOARCH)}"
version="${2:-${REMASK_CORE_VERSION:-}}"
output="${3:-$core_dir/dist}"
runtime_library="${REMASK_ONNXRUNTIME_LIBRARY:-}"
model_root="${REMASK_MODEL_ROOT:-$core_dir/models}"

die() { printf '[core-release] error: %s\n' "$*" >&2; exit 1; }
require() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }
sha256_file() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }

[[ "$target" =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid target triple: $target"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || die "version must be a semantic version (got '$version')"
require go
require tar
require node

case "$target" in
  *windows*) goos=windows; goarch=amd64; binary_name=remask-core.exe; archive_ext=tar.gz ;;
  *darwin*) goos=darwin; binary_name=remask-core; archive_ext=tar.gz ;;
  *linux*) goos=linux; binary_name=remask-core; archive_ext=tar.gz ;;
  *) die "unsupported target operating system: $target" ;;
esac
case "$target" in
  aarch64-*|arm64-*) goarch=arm64 ;;
  x86_64-*|amd64-*) goarch=amd64 ;;
  armv7*|arm-*) goarch=arm ;;
  i686-*|i386-*) goarch=386 ;;
esac

[[ -n "$runtime_library" && -f "$runtime_library" ]] || die "REMASK_ONNXRUNTIME_LIBRARY must point to the tested ONNX Runtime library"
mkdir -p "$output"
stage="$(mktemp -d "${TMPDIR:-/tmp}/remask-core-release.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/core/runtime"

ldflags=("-s" "-w" "-X" "github.com/remask/remask-core/internal/buildinfo.Version=$version" "-X" "github.com/remask/remask-core/internal/buildinfo.APIVersion=${REMASK_CORE_API_VERSION:-v1}" "-X" "github.com/remask/remask-core/internal/buildinfo.BuildID=${REMASK_CORE_BUILD_ID:-${GITHUB_RUN_ID:-private}}" "-X" "github.com/remask/remask-core/internal/buildinfo.BuildTime=${REMASK_CORE_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}")
if [[ -n "${REMASK_LICENSE_PUBLIC_KEY:-}" ]]; then
  [[ "$REMASK_LICENSE_PUBLIC_KEY" =~ ^[A-Za-z0-9+/=_-]+$ ]] || die "REMASK_LICENSE_PUBLIC_KEY must be Base64"
  ldflags+=("-X" "github.com/remask/remask-core/internal/license.EmbeddedPublicKey=$REMASK_LICENSE_PUBLIC_KEY" "-X" "github.com/remask/remask-core/internal/license.EmbeddedKeyID=${REMASK_LICENSE_KEY_ID:-prod-v1}")
fi
go_env=(GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=1)
if [[ -n "${REMASK_GO_CC:-}" ]]; then go_env+=(CC="$REMASK_GO_CC"); fi
env "${go_env[@]}" go build -trimpath -tags onnxruntime -ldflags "${ldflags[*]}" -o "$stage/core/$binary_name" ./cmd/remask-core
cp -L "$runtime_library" "$stage/core/runtime/$(basename "$runtime_library")"
runtime_dir="$(cd "$(dirname "$runtime_library")" && pwd)"
case "$goos" in
  darwin) provider_glob=("$runtime_dir"/libonnxruntime_providers_*.dylib) ;;
  linux) provider_glob=("$runtime_dir"/libonnxruntime_providers_*.so*) ;;
  windows) provider_glob=("$runtime_dir"/onnxruntime_providers_*.dll "$runtime_dir"/DirectML.dll) ;;
  *) provider_glob=() ;;
esac
for provider in "${provider_glob[@]}"; do
  [[ -f "$provider" ]] || continue
  cp -L "$provider" "$stage/core/runtime/$(basename "$provider")"
done
chmod 0755 "$stage/core/$binary_name"

cat > "$stage/core/VERSION" <<EOF
$version
EOF
cat > "$stage/core/runtime-manifest.json" <<EOF
{
  "schemaVersion": 1,
  "coreVersion": "$version",
  "apiVersion": "${REMASK_CORE_API_VERSION:-v1}",
  "target": "$target",
  "binary": "$binary_name",
  "binarySha256": "$(sha256_file "$stage/core/$binary_name")",
  "runtime": "$(basename "$runtime_library")",
  "runtimeSha256": "$(sha256_file "$runtime_library")"
}
EOF

asset="remask-core-v${version}-${target}.${archive_ext}"
tar -czf "$output/$asset" -C "$stage" core
asset_sha="$(sha256_file "$output/$asset")"
cat > "$output/${target}.json" <<EOF
{
  "target": "$target",
  "asset": "$asset",
  "sha256": "$asset_sha",
  "binary": "core/$binary_name"
}
EOF
printf '%s  %s\n' "$asset_sha" "$asset" >> "$output/SHA256SUMS"
printf '[core-release] wrote %s\n' "$output/$asset"
