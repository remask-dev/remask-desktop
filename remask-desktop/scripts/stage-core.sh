#!/usr/bin/env bash
set -euo pipefail

desktop_dir="$(cd "$(dirname "$0")/.." && pwd)"
workspace_dir="$(cd "$desktop_dir/.." && pwd)"
core_dir="$workspace_dir/remask-core"
tauri_dir="$desktop_dir/src-tauri"
target_triple="${TARGET_TRIPLE:-$(rustc -vV | awk '/^host:/ { print $2 }')}"
runtime_library="${REMASK_ONNXRUNTIME_LIBRARY:-}"
model_ids="${REMASK_MODEL_IDS:-openai-privacy-filter-q4f16}"
go_cc="${REMASK_GO_CC:-${CC:-}}"
runtime_target_dir="$tauri_dir/resources/onnxruntime"
models_stage_dir="$tauri_dir/resources/models.stage"

if [[ ! "$target_triple" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid TARGET_TRIPLE: $target_triple" >&2
  exit 1
fi
case "$target_triple" in
  *windows*) target_goos="windows" ;;
  *darwin*) target_goos="darwin" ;;
  *linux*) target_goos="linux" ;;
  *)
    echo "unsupported TARGET_TRIPLE OS: $target_triple" >&2
    exit 1
    ;;
esac
case "$target_triple" in
  aarch64-*|arm64-*) target_goarch="arm64" ;;
  x86_64-*|amd64-*) target_goarch="amd64" ;;
  i686-*|i386-*) target_goarch="386" ;;
  armv7*|arm-*) target_goarch="arm" ;;
  *)
    echo "unsupported TARGET_TRIPLE architecture: $target_triple" >&2
    exit 1
    ;;
esac

case "$target_goos" in
  darwin) runtime_filename="libonnxruntime.dylib" ;;
  linux) runtime_filename="libonnxruntime.so" ;;
  windows) runtime_filename="onnxruntime.dll" ;;
  *)
    echo "unsupported target platform: $target_goos" >&2
    exit 1
    ;;
esac

host_goos="$(go env GOOS)"
if [[ "$target_goos" != "$host_goos" && -z "$go_cc" ]]; then
  echo "cross-compiling the ONNX-enabled Core requires a target C compiler; set REMASK_GO_CC" >&2
  exit 1
fi

sidecar_path="$tauri_dir/binaries/remask-core-$target_triple"
if [[ "$target_goos" == "windows" ]]; then
  sidecar_path+=".exe"
fi

# Local desktop builds normally reuse the runtime staged by the previous
# successful build. CI and cross-compilation can still provide an explicit
# source through REMASK_ONNXRUNTIME_LIBRARY.
if [[ -z "$runtime_library" && -f "$runtime_target_dir/$runtime_filename" ]]; then
  runtime_library="$runtime_target_dir/$runtime_filename"
fi

if [[ -z "$target_triple" ]]; then
  echo "unable to determine Rust target triple; set TARGET_TRIPLE" >&2
  exit 1
fi
if [[ -z "$runtime_library" || ! -f "$runtime_library" ]]; then
  echo "set REMASK_ONNXRUNTIME_LIBRARY to the platform ONNX Runtime shared library" >&2
  exit 1
fi

mkdir -p "$tauri_dir/binaries" "$runtime_target_dir"
rm -rf "$models_stage_dir"
mkdir -p "$models_stage_dir"

(
  cd "$core_dir"
  go_build_env=(GOOS="$target_goos" GOARCH="$target_goarch" CGO_ENABLED=1)
  if [[ -n "$go_cc" ]]; then
    go_build_env+=(CC="$go_cc")
  fi
  env "${go_build_env[@]}" go build -tags onnxruntime -o "$sidecar_path" ./cmd/remask-core
)

old_ifs="$IFS"
IFS=','
for model_id in $model_ids; do
  IFS="$old_ifs"
  model_id="$(printf '%s' "$model_id" | tr -d '[:space:]')"
  if [[ ! "$model_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "invalid model id: $model_id" >&2
    exit 1
  fi
  model_source="$core_dir/models/$model_id"
  model_manifest="$model_source/manifest.json"
  if [[ ! -f "$model_manifest" ]]; then
    echo "model package not found: $model_id" >&2
    exit 1
  fi
  if ! grep -q "\"id\"[[:space:]]*:[[:space:]]*\"$model_id\"" "$model_manifest"; then
    echo "manifest id does not match directory: $model_id" >&2
    exit 1
  fi
  model_target="$models_stage_dir/$model_id"
  rm -rf "$model_target"
  cp -R "$model_source" "$model_target"
  echo "staged model $model_id"
  IFS=','
done
IFS="$old_ifs"
rm -rf "$tauri_dir/resources/models"
mv "$models_stage_dir" "$tauri_dir/resources/models"
runtime_source_dir="$(cd "$(dirname "$runtime_library")" && pwd)"
runtime_source_path="$runtime_source_dir/$(basename "$runtime_library")"
runtime_target_path="$(cd "$runtime_target_dir" && pwd)/$runtime_filename"
# When the staged runtime is the source, leave its provider companions in
# place. An explicit external runtime remains authoritative and replaces any
# provider files left by an earlier build.
if [[ "$runtime_source_path" != "$runtime_target_path" ]]; then
  # Cross-platform staging can reuse this directory between builds. Remove
  # main runtime libraries for other operating systems so a Windows bundle
  # cannot accidentally include a previously staged macOS or Linux runtime.
  stale_runtime_libraries=(
    "$runtime_target_dir/libonnxruntime.dylib"
    "$runtime_target_dir/libonnxruntime.so"
    "$runtime_target_dir/onnxruntime.dll"
  )
  for stale_runtime_library in "${stale_runtime_libraries[@]}"; do
    rm -f "$stale_runtime_library"
  done

  # Remove provider files from an earlier staging run before copying the new
  # runtime set; otherwise switching from a GPU runtime to a CPU runtime could
  # leave stale providers in the application bundle.
  shopt -s nullglob
  stale_provider_libraries=(
    "$runtime_target_dir"/libonnxruntime_providers_*
    "$runtime_target_dir"/onnxruntime_providers_*.dll
    "$runtime_target_dir"/DirectML.dll
  )
  shopt -u nullglob
  for stale_provider_library in "${stale_provider_libraries[@]}"; do
    rm -f "$stale_provider_library"
  done
  rm -f "$runtime_target_path"
  cp "$runtime_source_path" "$runtime_target_path"

  # GPU execution providers may be shipped as companion libraries beside the
  # main ONNX Runtime library. Stage them with the sidecar so providers can be
  # loaded at runtime (CUDA/ROCm/OpenVINO on Linux, CUDA/DirectML on Windows).
  provider_libraries=()
  case "$runtime_filename" in
    libonnxruntime.dylib)
      shopt -s nullglob
      provider_libraries=("$runtime_source_dir"/libonnxruntime_providers_*.dylib)
      shopt -u nullglob
      ;;
    libonnxruntime.so)
      shopt -s nullglob
      provider_libraries=("$runtime_source_dir"/libonnxruntime_providers_*.so*)
      shopt -u nullglob
      ;;
    onnxruntime.dll)
      shopt -s nullglob
      provider_libraries=("$runtime_source_dir"/onnxruntime_providers_*.dll "$runtime_source_dir"/DirectML.dll)
      shopt -u nullglob
      ;;
    *)
      provider_libraries=()
      ;;
  esac
  if [[ -n "${provider_libraries[*]-}" ]]; then
    for provider_library in "${provider_libraries[@]}"; do
      [[ -f "$provider_library" ]] || continue
      cp -L "$provider_library" "$runtime_target_dir/$(basename "$provider_library")"
    done
  fi
fi
# Downloaded runtimes may arrive read-only; tauri-build copies resources with
# fs::copy, which cannot overwrite a read-only destination on a rebuild.
chmod u+w "$runtime_target_dir/$runtime_filename"

echo "staged remask-core-$target_triple"
echo "staged runtime $runtime_filename"
