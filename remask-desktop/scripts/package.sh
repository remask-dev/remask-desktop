#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
desktop_dir="$(cd "$script_dir/.." && pwd)"
workspace_dir="$(cd "$desktop_dir/.." && pwd)"
tauri_dir="$desktop_dir/src-tauri"
artifacts_dir="$workspace_dir/artifacts"

log() {
  printf '[package] %s\n' "$*"
}

die() {
  printf '[package] error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

normalize_arch() {
  case "$1" in
    x86_64|amd64|x64) printf 'x64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) die "unsupported architecture: $1" ;;
  esac
}

detect_host_platform() {
  case "$(uname -s)" in
    Darwin) printf 'macos\n' ;;
    Linux) printf 'linux\n' ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT) printf 'windows\n' ;;
    *) die "unsupported build host: $(uname -s)" ;;
  esac
}

ensure_rust_target() {
  if ! rustup target list --installed | grep -qx "$1"; then
    log "installing Rust target $1"
    rustup target add "$1"
  fi
}

resolve_runtime_library() {
  local platform="$1"
  local runtime="${REMASK_ONNXRUNTIME_LIBRARY:-}"
  local filename variable_name

  case "$platform" in
    macos)
      runtime="${runtime:-${REMASK_ONNXRUNTIME_MACOS_LIBRARY:-}}"
      filename="libonnxruntime.dylib"
      variable_name="REMASK_ONNXRUNTIME_MACOS_LIBRARY"
      ;;
    windows)
      runtime="${runtime:-${REMASK_ONNXRUNTIME_WINDOWS_LIBRARY:-}}"
      filename="onnxruntime.dll"
      variable_name="REMASK_ONNXRUNTIME_WINDOWS_LIBRARY"
      ;;
    linux)
      runtime="${runtime:-${REMASK_ONNXRUNTIME_LINUX_LIBRARY:-}}"
      filename="libonnxruntime.so"
      variable_name="REMASK_ONNXRUNTIME_LINUX_LIBRARY"
      ;;
    *) die "unsupported runtime platform: $platform" ;;
  esac

  if [[ -z "$runtime" && -f "$tauri_dir/resources/onnxruntime/$filename" ]]; then
    runtime="$tauri_dir/resources/onnxruntime/$filename"
  fi
  [[ -n "$runtime" ]] || die "set $variable_name or REMASK_ONNXRUNTIME_LIBRARY"
  [[ -f "$runtime" ]] || die "ONNX Runtime library not found: $runtime"
  printf '%s\n' "$runtime"
}

configure_windows_cross_toolchain() {
  local toolchain_dir="${REMASK_WINDOWS_TOOLCHAIN_DIR:-${LLVM_MINGW_DIR:-}}"
  local compiler rustflags

  if [[ -n "$toolchain_dir" ]]; then
    compiler="$toolchain_dir/bin/x86_64-w64-mingw32-clang"
  else
    compiler="$(command -v x86_64-w64-mingw32-clang || true)"
    if [[ -n "$compiler" ]]; then
      toolchain_dir="$(cd "$(dirname "$compiler")/.." && pwd)"
    fi
  fi

  [[ -n "$toolchain_dir" ]] || die "set REMASK_WINDOWS_TOOLCHAIN_DIR to an LLVM-MinGW installation"
  [[ -x "$toolchain_dir/bin/x86_64-w64-mingw32-clang" ]] || die "LLVM-MinGW clang not found under $toolchain_dir/bin"
  [[ -x "$toolchain_dir/bin/x86_64-w64-mingw32-gcc" ]] || die "LLVM-MinGW gcc driver not found under $toolchain_dir/bin"

  export PATH="$toolchain_dir/bin:$PATH"
  export REMASK_GO_CC="${REMASK_GO_CC:-$toolchain_dir/bin/x86_64-w64-mingw32-gcc}"
  export CARGO_TARGET_X86_64_PC_WINDOWS_GNULLVM_LINKER="$toolchain_dir/bin/x86_64-w64-mingw32-clang"
  rustflags="${CARGO_TARGET_X86_64_PC_WINDOWS_GNULLVM_RUSTFLAGS:-}"
  if [[ "$rustflags" != *"target-feature=+crt-static"* ]]; then
    rustflags="${rustflags:+$rustflags }-C target-feature=+crt-static"
  fi
  export CARGO_TARGET_X86_64_PC_WINDOWS_GNULLVM_RUSTFLAGS="$rustflags"
  export CC_x86_64_pc_windows_gnullvm="$toolchain_dir/bin/x86_64-w64-mingw32-clang"
  export CXX_x86_64_pc_windows_gnullvm="$toolchain_dir/bin/x86_64-w64-mingw32-clang++"
  export AR_x86_64_pc_windows_gnullvm="$toolchain_dir/bin/x86_64-w64-mingw32-ar"

  require_command makensis
}

verify_windows_runtime_dependencies() {
  local target="$1"
  local executable="$tauri_dir/target/$target/release/remask-desktop.exe"
  local loader="$tauri_dir/target/$target/release/WebView2Loader.dll"
  local dependencies

  [[ -f "$executable" ]] || die "Windows executable not found: $executable"
  if [[ "$target" != "x86_64-pc-windows-gnullvm" ]]; then
    return
  fi

  require_command llvm-objdump
  dependencies="$(llvm-objdump -p "$executable")"
  if grep -qi 'DLL Name: libunwind\.dll' <<<"$dependencies"; then
    die "Windows executable dynamically links libunwind.dll; static CRT linking is required"
  fi
  if grep -qi 'DLL Name: WebView2Loader\.dll' <<<"$dependencies"; then
    [[ -f "$loader" ]] || die "WebView2Loader.dll not found beside the Windows executable"
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

collect_artifacts() {
  local platform="$1"
  local arch="$2"
  local target="$3"
  local version destination bundle_root source_file filename
  local -a files=()

  version="$(node -p "require('$desktop_dir/package.json').version")"
  destination="$artifacts_dir/$platform/$arch/$version"
  bundle_root="$tauri_dir/target/$target/release/bundle"
  mkdir -p "$destination"
  find "$destination" -maxdepth 1 -type f \( \
    -name '*.dmg' -o -name '*.exe' -o -name '*.deb' -o \
    -name '*.rpm' -o -name '*.AppImage' -o -name 'SHA256SUMS' \
  \) -delete

  case "$platform" in
    macos)
      while IFS= read -r -d '' source_file; do files+=("$source_file"); done < <(
        find "$bundle_root/dmg" -maxdepth 1 -type f -name '*.dmg' -print0 2>/dev/null
      )
      ;;
    windows)
      while IFS= read -r -d '' source_file; do files+=("$source_file"); done < <(
        find "$bundle_root/nsis" -maxdepth 1 -type f -name '*.exe' -print0 2>/dev/null
      )
      ;;
    linux)
      while IFS= read -r -d '' source_file; do files+=("$source_file"); done < <(
        find "$bundle_root" -type f \( -name '*.deb' -o -name '*.rpm' -o -name '*.AppImage' \) -print0 2>/dev/null
      )
      ;;
  esac

  ((${#files[@]} > 0)) || die "no package artifacts found under $bundle_root"

  : > "$destination/SHA256SUMS"
  for source_file in "${files[@]}"; do
    filename="$(basename "$source_file")"
    cp "$source_file" "$destination/$filename"
    (
      cd "$destination"
      sha256_file "$filename" >> SHA256SUMS
    )
    log "artifact: $destination/$filename"
  done
  log "checksums: $destination/SHA256SUMS"
}

require_command go
require_command node
require_command npm
require_command rustc
require_command rustup
[[ -x "$desktop_dir/node_modules/.bin/tauri" ]] || die "desktop dependencies are missing; run npm ci"

host_platform="$(detect_host_platform)"
host_arch="$(normalize_arch "$(uname -m)")"
platform="${1:-current}"
if [[ "$platform" == "current" ]]; then
  platform="$host_platform"
fi
case "$platform" in
  mac|darwin) platform="macos" ;;
  win) platform="windows" ;;
esac

if [[ -n "${REMASK_ARCH:-}" ]]; then
  arch="$(normalize_arch "$REMASK_ARCH")"
elif [[ "$platform" == "$host_platform" ]]; then
  arch="$host_arch"
else
  arch="x64"
fi

case "$platform:$arch" in
  macos:arm64) target="aarch64-apple-darwin" ; bundles="${REMASK_MACOS_BUNDLES:-dmg}" ;;
  macos:x64) target="x86_64-apple-darwin" ; bundles="${REMASK_MACOS_BUNDLES:-dmg}" ;;
  linux:arm64) target="aarch64-unknown-linux-gnu" ; bundles="${REMASK_LINUX_BUNDLES:-deb,appimage}" ;;
  linux:x64) target="x86_64-unknown-linux-gnu" ; bundles="${REMASK_LINUX_BUNDLES:-deb,appimage}" ;;
  windows:arm64)
    [[ "$host_platform" == "windows" ]] || die "cross-building Windows ARM64 is not supported by this script"
    target="aarch64-pc-windows-msvc"
    bundles="${REMASK_WINDOWS_BUNDLES:-nsis}"
    ;;
  windows:x64)
    if [[ "$host_platform" == "windows" ]]; then
      target="x86_64-pc-windows-msvc"
    else
      target="x86_64-pc-windows-gnullvm"
      configure_windows_cross_toolchain
    fi
    bundles="${REMASK_WINDOWS_BUNDLES:-nsis}"
    ;;
  *) die "unsupported package target: $platform/$arch" ;;
esac

if [[ "$platform" == "macos" && "$host_platform" != "macos" ]]; then
  die "macOS packages must be built on macOS"
fi
if [[ "$platform" == "linux" && "$host_platform" != "linux" ]]; then
  die "Linux packages must be built on Linux or a Linux CI runner"
fi
if [[ "$platform" == "$host_platform" && "$arch" != "$host_arch" && -z "${REMASK_GO_CC:-}" ]]; then
  die "cross-architecture Core builds require REMASK_GO_CC for $arch"
fi

ensure_rust_target "$target"
runtime_library="$(resolve_runtime_library "$platform")"

log "platform=$platform arch=$arch target=$target bundles=$bundles"
log "staging Core, models, and ONNX Runtime"
TARGET_TRIPLE="$target" \
REMASK_ONNXRUNTIME_LIBRARY="$runtime_library" \
bash "$script_dir/stage-core.sh"

tauri_args=(build --target "$target" --bundles "$bundles")
if [[ "${REMASK_SIGN:-0}" != "1" ]]; then
  tauri_args+=(--no-sign)
fi

# LLVM-MinGW's WebView2 import loader is a runtime DLL. Tauri resources are
# normally installed below resources/, but Windows resolves this DLL beside
# the application executable, so map it to the installation root.
if [[ "$target" == "x86_64-pc-windows-gnullvm" ]]; then
  windows_runtime_config="{\"bundle\":{\"resources\":{\"resources/models/**/*\":\"resources/models/\",\"resources/onnxruntime/**/*\":\"resources/onnxruntime/\",\"target/$target/release/WebView2Loader.dll\":\"\"}}}"
  tauri_args+=(--config "$windows_runtime_config")
fi

# Tauri synchronizes platform-specific features by editing Cargo.toml. A
# Windows cross-build would otherwise remove macos-private-api and leave the
# developer worktree dirty, so preserve the manifest around the CLI call.
cargo_manifest="$tauri_dir/Cargo.toml"
cargo_manifest_backup="$(mktemp "${TMPDIR:-/tmp}/remask-cargo-manifest.XXXXXX")"
cp "$cargo_manifest" "$cargo_manifest_backup"
restore_cargo_manifest() {
  if [[ -f "$cargo_manifest_backup" ]]; then
    cp "$cargo_manifest_backup" "$cargo_manifest"
    rm -f "$cargo_manifest_backup"
  fi
}
trap restore_cargo_manifest EXIT

log "building Tauri package"
(
  cd "$desktop_dir"
  npx tauri "${tauri_args[@]}"
)
restore_cargo_manifest
trap - EXIT

if [[ "$platform" == "windows" ]]; then
  verify_windows_runtime_dependencies "$target"
fi
collect_artifacts "$platform" "$arch" "$target"
