#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
desktop_dir="$(cd "$script_dir/.." && pwd)"
workspace_dir="$(cd "$desktop_dir/.." && pwd)"
tauri_dir="$desktop_dir/src-tauri"
artifacts_dir="$workspace_dir/artifacts"
packaging_lock="$script_dir/packaging.lock.json"
windows_runtime_stage_dir="$tauri_dir/resources/windows-runtime"
windows_vc_runtime_files=(
  msvcp140.dll
  msvcp140_1.dll
  vcruntime140.dll
  vcruntime140_1.dll
)

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

lock_value() {
  node -e '
    const lock = require(process.argv[1]);
    let value = lock;
    for (const key of process.argv[2].split(".")) value = value?.[key];
    if (value === undefined || value === null) process.exit(2);
    process.stdout.write(String(value));
  ' "$packaging_lock" "$1"
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
  local arch="$2"
  local runtime="${REMASK_ONNXRUNTIME_LIBRARY:-}"
  local filename variable_name target_key locked_path locked_sha256 expected_sha256
  local cache_dir archive package_url package_sha256 extract_dir native_dir

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

  target_key="$platform-$arch"
  locked_path="$(lock_value "onnxRuntime.targets.$target_key.path" 2>/dev/null || true)"
  locked_sha256="$(lock_value "onnxRuntime.targets.$target_key.sha256" 2>/dev/null || true)"

  if [[ -n "$runtime" ]]; then
    [[ -f "$runtime" ]] || die "ONNX Runtime library not found: $runtime"
    expected_sha256="${REMASK_ONNXRUNTIME_SHA256:-$locked_sha256}"
    [[ -n "$expected_sha256" ]] || die "set REMASK_ONNXRUNTIME_SHA256 for custom $platform/$arch runtime"
    resolved_runtime_library="$runtime"
    resolved_runtime_sha256="$expected_sha256"
    return
  fi

  [[ -n "$locked_path" && -n "$locked_sha256" ]] || die "no locked ONNX Runtime for $platform/$arch; set $variable_name and REMASK_ONNXRUNTIME_SHA256"
  require_command curl
  require_command unzip
  package_url="$(lock_value onnxRuntime.package.url)"
  package_sha256="$(lock_value onnxRuntime.package.sha256)"
  cache_dir="${REMASK_PACKAGE_CACHE_DIR:-$desktop_dir/.cache}/onnxruntime-$(lock_value onnxRuntime.version)"
  archive="$cache_dir/microsoft.ml.onnxruntime.nupkg"
  extract_dir="$cache_dir/extracted"
  mkdir -p "$cache_dir"
  if [[ ! -f "$archive" || "$(sha256_file "$archive" | awk '{print $1}')" != "$package_sha256" ]]; then
    log "downloading locked ONNX Runtime $(lock_value onnxRuntime.version)"
    curl -fL --retry 3 -o "$archive.part" "$package_url"
    if [[ "$(sha256_file "$archive.part" | awk '{print $1}')" != "$package_sha256" ]]; then
      rm -f "$archive.part"
      die "ONNX Runtime package checksum mismatch"
    fi
    mv "$archive.part" "$archive"
  fi
  runtime="$extract_dir/$locked_path"
  if [[ ! -f "$runtime" || "$(sha256_file "$runtime" | awk '{print $1}')" != "$locked_sha256" ]]; then
    native_dir="$(dirname "$locked_path")"
    rm -rf "$extract_dir.stage"
    mkdir -p "$extract_dir.stage"
    unzip -q "$archive" "$native_dir/*" -d "$extract_dir.stage"
    rm -rf "$extract_dir"
    mv "$extract_dir.stage" "$extract_dir"
  fi
  [[ -f "$runtime" ]] || die "locked ONNX Runtime was not extracted: $runtime"
  resolved_runtime_library="$runtime"
  resolved_runtime_sha256="$locked_sha256"
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

stage_windows_vc_runtime() {
  local arch="$1"
  local runtime_source="${REMASK_WINDOWS_VC_RUNTIME_DIR:-}"
  local cache_dir archive archive_sha256 download_url candidate filename
  local -a candidates=()

  if [[ -z "$runtime_source" && -n "${VCToolsRedistDir:-}" ]]; then
    candidates+=(
      "$VCToolsRedistDir/$arch/Microsoft.VC143.CRT"
      "$VCToolsRedistDir/$arch/Microsoft.VC142.CRT"
    )
  fi
  if [[ -z "$runtime_source" ]]; then
    if ((${#candidates[@]} > 0)); then
      for candidate in "${candidates[@]}"; do
        if [[ -f "$candidate/msvcp140.dll" && -f "$candidate/vcruntime140.dll" ]]; then
          runtime_source="$candidate"
          break
        fi
      done
    fi
  fi

  if [[ -z "$runtime_source" ]]; then
    [[ "$arch" == "x64" ]] || die "set REMASK_WINDOWS_VC_RUNTIME_DIR for Windows $arch packaging"
    require_command curl
    require_command unzip
    cache_dir="${REMASK_PACKAGE_CACHE_DIR:-$desktop_dir/.cache}/windows-vclibs-x64-14.00-desktop"
    archive="$cache_dir/Microsoft.VCLibs.x64.14.00.Desktop.appx"
    archive_sha256="$(lock_value windowsVCLibs.x64.sha256)"
    download_url="$(lock_value windowsVCLibs.x64.url)"
    mkdir -p "$cache_dir"
    if [[ ! -f "$archive" || "$(sha256_file "$archive" | awk '{print $1}')" != "$archive_sha256" ]]; then
      log "downloading Microsoft Visual C++ x64 runtime"
      curl -fL --retry 3 -o "$archive.part" "$download_url"
      if [[ "$(sha256_file "$archive.part" | awk '{print $1}')" != "$archive_sha256" ]]; then
        rm -f "$archive.part"
        die "Microsoft Visual C++ runtime checksum mismatch"
      fi
      mv "$archive.part" "$archive"
    fi
    runtime_source="$cache_dir/files"
    if [[ ! -f "$runtime_source/msvcp140.dll" || ! -f "$runtime_source/vcruntime140_1.dll" ]]; then
      rm -rf "$runtime_source.stage"
      mkdir -p "$runtime_source.stage"
      unzip -q -j "$archive" "${windows_vc_runtime_files[@]}" -d "$runtime_source.stage"
      rm -rf "$runtime_source"
      mv "$runtime_source.stage" "$runtime_source"
    fi
  fi

  rm -rf "$windows_runtime_stage_dir"
  mkdir -p "$windows_runtime_stage_dir"
  for filename in "${windows_vc_runtime_files[@]}"; do
    [[ -f "$runtime_source/$filename" ]] || die "Windows VC++ runtime not found: $runtime_source/$filename"
    cp "$runtime_source/$filename" "$windows_runtime_stage_dir/$filename"
  done
  log "staged Microsoft Visual C++ runtime"
}

verify_windows_runtime_dependencies() {
  local target="$1"
  local executable="$tauri_dir/target/$target/release/remask-desktop.exe"
  local loader="$tauri_dir/target/$target/release/WebView2Loader.dll"
  local dependencies filename

  [[ -f "$executable" ]] || die "Windows executable not found: $executable"
  for filename in "${windows_vc_runtime_files[@]}"; do
    [[ -f "$windows_runtime_stage_dir/$filename" ]] || die "Windows VC++ runtime was not staged: $filename"
  done
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
  last_artifacts_dir="$destination"
}

validate_signing_configuration() {
  local platform="$1"
  local credentials_configured=0

  case "$platform" in
    macos)
      require_command security
      [[ -n "${APPLE_SIGNING_IDENTITY:-}" ]] || die "APPLE_SIGNING_IDENTITY is required for a signed macOS release"
      if ! security find-identity -v -p codesigning | grep -F "${APPLE_SIGNING_IDENTITY}" >/dev/null; then
        die "APPLE_SIGNING_IDENTITY is not available in the macOS keychain"
      fi

      if [[ "$release_enabled" == "1" ]]; then
        if [[ -n "${APPLE_API_ISSUER:-}" || -n "${APPLE_API_KEY:-}" || -n "${APPLE_API_KEY_PATH:-}" ]]; then
          [[ -n "${APPLE_API_ISSUER:-}" && -n "${APPLE_API_KEY:-}" && -n "${APPLE_API_KEY_PATH:-}" ]] || \
            die "APPLE_API_ISSUER, APPLE_API_KEY, and APPLE_API_KEY_PATH must all be set for notarization"
          [[ -f "${APPLE_API_KEY_PATH}" ]] || die "Apple notarization key not found: ${APPLE_API_KEY_PATH}"
          credentials_configured=1
        elif [[ -n "${APPLE_ID:-}" || -n "${APPLE_PASSWORD:-}" || -n "${APPLE_TEAM_ID:-}" ]]; then
          [[ -n "${APPLE_ID:-}" && -n "${APPLE_PASSWORD:-}" && -n "${APPLE_TEAM_ID:-}" ]] || \
            die "APPLE_ID, APPLE_PASSWORD, and APPLE_TEAM_ID must all be set for notarization"
          credentials_configured=1
        fi
        ((credentials_configured == 1)) || die "Apple notarization credentials are required for a macOS release"
      fi
      ;;
    windows)
      [[ "${REMASK_WINDOWS_CERTIFICATE_THUMBPRINT:-}" =~ ^[A-Fa-f0-9]{40}$ ]] || \
        die "REMASK_WINDOWS_CERTIFICATE_THUMBPRINT must be a 40-character SHA-1 certificate thumbprint"
      ;;
  esac
}

verify_release_artifact_signatures() {
  local platform="$1"
  local file

  case "$platform" in
    macos)
      require_command spctl
      require_command xcrun
      while IFS= read -r -d '' file; do
        spctl --assess --type open --context context:primary-signature --verbose=2 "$file"
        xcrun stapler validate "$file"
        log "verified signed and notarized artifact: $file"
      done < <(find "$last_artifacts_dir" -maxdepth 1 -type f -name '*.dmg' -print0)
      ;;
    windows)
      require_command powershell.exe
      while IFS= read -r -d '' file; do
        REMASK_SIGNATURE_FILE="$file" powershell.exe -NoProfile -NonInteractive -Command '
          $signature = Get-AuthenticodeSignature -LiteralPath $env:REMASK_SIGNATURE_FILE
          if ($signature.Status -ne "Valid") {
            throw "invalid Authenticode signature for $($env:REMASK_SIGNATURE_FILE): $($signature.Status)"
          }
        '
        log "verified Authenticode signature: $file"
      done < <(find "$last_artifacts_dir" -maxdepth 1 -type f -name '*.exe' -print0)
      ;;
  esac
}

notarize_macos_disk_images() {
  local target="$1"
  local bundle_dir="$tauri_dir/target/$target/release/bundle/dmg"
  local file
  local -a credentials=()
  local -a files=()

  require_command xcrun
  if [[ -n "${APPLE_API_ISSUER:-}" ]]; then
    credentials=(
      --issuer "$APPLE_API_ISSUER"
      --key-id "$APPLE_API_KEY"
      --key "$APPLE_API_KEY_PATH"
    )
  else
    credentials=(
      --apple-id "$APPLE_ID"
      --password "$APPLE_PASSWORD"
      --team-id "$APPLE_TEAM_ID"
    )
  fi

  while IFS= read -r -d '' file; do files+=("$file"); done < <(
    find "$bundle_dir" -maxdepth 1 -type f -name '*.dmg' -print0 2>/dev/null
  )
  ((${#files[@]} > 0)) || die "no macOS disk image found for notarization under $bundle_dir"

  for file in "${files[@]}"; do
    log "submitting final disk image for notarization: $file"
    xcrun notarytool submit "$file" "${credentials[@]}" --wait
    xcrun stapler staple "$file"
    xcrun stapler validate "$file"
    log "notarized and stapled final disk image: $file"
  done
}

require_command go
require_command node
require_command npm
require_command rustc
require_command rustup
[[ -f "$packaging_lock" ]] || die "packaging lock not found: $packaging_lock"
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
if [[ "${REMASK_RELEASE:-0}" == "1" && "$platform" != "$host_platform" ]]; then
  die "release packages must be built and smoke-tested on their native platform"
fi
if [[ "$platform" == "$host_platform" && "$arch" != "$host_arch" && -z "${REMASK_GO_CC:-}" ]]; then
  die "cross-architecture Core builds require REMASK_GO_CC for $arch"
fi

release_enabled="${REMASK_RELEASE:-0}"
sign_enabled="${REMASK_SIGN:-0}"
[[ "$release_enabled" == "0" || "$release_enabled" == "1" ]] || die "REMASK_RELEASE must be 0 or 1"
[[ "$sign_enabled" == "0" || "$sign_enabled" == "1" ]] || die "REMASK_SIGN must be 0 or 1"
if [[ "$release_enabled" == "1" && ("$platform" == "macos" || "$platform" == "windows") ]]; then
  [[ "$sign_enabled" != "0" || -z "${REMASK_SIGN+x}" ]] || die "release packages for $platform cannot disable signing"
  sign_enabled=1
fi
if [[ "$sign_enabled" == "1" ]]; then
  validate_signing_configuration "$platform"
fi

ensure_rust_target "$target"
resolve_runtime_library "$platform" "$arch"
runtime_library="$resolved_runtime_library"
model_ids="${REMASK_MODEL_IDS:-openai-privacy-filter-q4f16}"
node "$script_dir/verify-packaging-inputs.mjs" \
  "$packaging_lock" "$runtime_library" "$resolved_runtime_sha256" \
  "$workspace_dir/remask-core/models" "$model_ids"

log "platform=$platform arch=$arch target=$target bundles=$bundles"
if [[ "$platform" == "windows" ]]; then
  stage_windows_vc_runtime "$arch"
fi
log "staging Core, models, and ONNX Runtime"
TARGET_TRIPLE="$target" \
REMASK_ONNXRUNTIME_LIBRARY="$runtime_library" \
REMASK_MODEL_IDS="$model_ids" \
bash "$script_dir/stage-core.sh"

tauri_args=(build --target "$target" --bundles "$bundles")
if [[ "$sign_enabled" != "1" ]]; then
  tauri_args+=(--no-sign)
fi

# Windows resolves the WebView2 loader and ONNX Runtime's VC++ dependencies
# beside the application executable. Map those DLLs to the installation root
# instead of Tauri's normal resources/ directory.
if [[ "$platform" == "windows" ]]; then
  windows_resource_map="\"resources/models/**/*\":\"resources/models/\",\"resources/onnxruntime/**/*\":\"resources/onnxruntime/\",\"resources/windows-runtime/*.dll\":\"\""
  if [[ "$target" == "x86_64-pc-windows-gnullvm" ]]; then
    windows_resource_map+=",\"target/$target/release/WebView2Loader.dll\":\"\""
  fi
  windows_signing_config=""
  if [[ "$sign_enabled" == "1" ]]; then
    windows_timestamp_url="${REMASK_WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}"
    [[ "$windows_timestamp_url" =~ ^https?:// ]] || die "REMASK_WINDOWS_TIMESTAMP_URL must be an HTTP(S) URL"
    windows_signing_config=",\"windows\":{\"certificateThumbprint\":\"${REMASK_WINDOWS_CERTIFICATE_THUMBPRINT}\",\"digestAlgorithm\":\"sha256\",\"timestampUrl\":\"${windows_timestamp_url}\"}"
  fi
  windows_runtime_config="{\"bundle\":{\"resources\":{$windows_resource_map}$windows_signing_config}}"
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
if [[ "$platform" == "macos" && "$release_enabled" == "1" && "$sign_enabled" == "1" ]]; then
  notarize_macos_disk_images "$target"
fi
collect_artifacts "$platform" "$arch" "$target"
if [[ "$release_enabled" == "1" && "$sign_enabled" == "1" ]]; then
  verify_release_artifact_signatures "$platform"
fi
