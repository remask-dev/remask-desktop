#!/usr/bin/env bash
set -euo pipefail

desktop_dir="$(cd "$(dirname "$0")/.." && pwd)"
workspace_dir="$(cd "$desktop_dir/.." && pwd)"
core_dir="$workspace_dir/remask-core"
tauri_dir="$desktop_dir/src-tauri"
target_triple="${TARGET_TRIPLE:-$(rustc -vV | awk '/^host:/ { print $2 }')}"
runtime_library="${REMASK_ONNXRUNTIME_LIBRARY:-}"
model_ids="${REMASK_MODEL_IDS:-openai-privacy-filter-q4}"
active_model_id="${REMASK_ACTIVE_MODEL:-openai-privacy-filter-q4}"
runtime_target_dir="$tauri_dir/resources/onnxruntime"
models_stage_dir="$tauri_dir/resources/models.stage"

if [[ ! "$target_triple" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid TARGET_TRIPLE: $target_triple" >&2
  exit 1
fi
if [[ ! "$active_model_id" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid REMASK_ACTIVE_MODEL: $active_model_id" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin) runtime_filename="libonnxruntime.dylib" ;;
  Linux) runtime_filename="libonnxruntime.so" ;;
  MINGW*|MSYS*|CYGWIN*) runtime_filename="onnxruntime.dll" ;;
  *)
    echo "unsupported host platform: $(uname -s)" >&2
    exit 1
    ;;
esac

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
  go build -tags onnxruntime -o "$tauri_dir/binaries/remask-core-$target_triple" ./cmd/remask-core
)

staged_active=false
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
  if [[ "$model_id" == "$active_model_id" ]]; then staged_active=true; fi
  IFS=','
done
IFS="$old_ifs"
if [[ "$staged_active" != true ]]; then
  echo "REMASK_ACTIVE_MODEL must be included in REMASK_MODEL_IDS" >&2
  exit 1
fi
printf '{\n  "active_model": "%s"\n}\n' "$active_model_id" > "$tauri_dir/resources/model-bundle.json"
rm -rf "$tauri_dir/resources/models"
mv "$models_stage_dir" "$tauri_dir/resources/models"
rm -f "$runtime_target_dir/$runtime_filename"
cp "$runtime_library" "$runtime_target_dir/$runtime_filename"

echo "staged remask-core-$target_triple"
echo "staged runtime $runtime_filename"
