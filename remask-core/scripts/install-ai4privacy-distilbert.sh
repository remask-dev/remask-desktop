#!/bin/sh
set -eu

revision="78dcbb58fbe1ea1a6419c06c4cfff1b4418f1b85"
package_id="ai4privacy-distilbert-q4"
models_dir="${1:-models}"
package_dir="$models_dir/$package_id"
base_url="${REMASK_MODEL_MIRROR:-https://hf-mirror.com}/onnx-community/distilbert_finetuned_ai4privacy_v2-ONNX/resolve/$revision"

mkdir -p "$package_dir"

download() {
  remote_path="$1"
  local_path="$2"
  temporary_path="$local_path.part"
  curl --fail --location --retry 3 --continue-at - "$base_url/$remote_path" --output "$temporary_path"
  mv "$temporary_path" "$local_path"
}

download "onnx/model_q4.onnx" "$package_dir/model_q4.onnx"
download "vocab.txt" "$package_dir/vocab.txt"
download "config.json" "$package_dir/config.json"
download "tokenizer_config.json" "$package_dir/tokenizer_config.json"

go run ./cmd/remask-model-packager \
  -dir "$package_dir" \
  -id "$package_id" \
  -name "AI4Privacy DistilBERT Q4" \
  -version "$revision"

echo "Installed $package_id in $package_dir"
