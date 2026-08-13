#!/bin/sh
set -eu

revision="7ffa9a043d54d1be65afb281eddf0ffbe629385b"
package_id="openai-privacy-filter-q4"
models_dir="${1:-models}"
package_dir="$models_dir/$package_id"
base_url="${REMASK_MODEL_MIRROR:-https://hf-mirror.com}/openai/privacy-filter/resolve/$revision"

mkdir -p "$package_dir"

download() {
  remote_path="$1"
  local_path="$2"
  temporary_path="$local_path.part"
  curl --fail --location --retry 3 --continue-at - "$base_url/$remote_path" --output "$temporary_path"
  mv "$temporary_path" "$local_path"
}

download "onnx/model_q4.onnx" "$package_dir/model_q4.onnx"
download "onnx/model_q4.onnx_data" "$package_dir/model_q4.onnx_data"
download "tokenizer.json" "$package_dir/tokenizer.json"
download "config.json" "$package_dir/config.json"
download "tokenizer_config.json" "$package_dir/tokenizer_config.json"
download "viterbi_calibration.json" "$package_dir/viterbi_calibration.json"

go run ./cmd/remask-model-packager \
  -dir "$package_dir" \
  -id "$package_id" \
  -name "OpenAI Privacy Filter Q4" \
  -version "$revision" \
  -quantization "q4" \
  -model "model_q4.onnx" \
  -vocab "tokenizer.json" \
  -tokenizer-type "o200k-base" \
  -label-scheme "BIOES" \
  -decoder "viterbi-bioes" \
  -calibration "viterbi_calibration.json" \
  -operating-point "default" \
  -max-tokens 512 \
  -stride 128 \
  -extra-files "model_data=model_q4.onnx_data,calibration=viterbi_calibration.json,config=config.json,tokenizer_config=tokenizer_config.json" \
  -entity-types "account_number=ACCOUNT_NUMBER,private_address=ADDRESS,private_date=PRIVATE_DATE,private_email=EMAIL_ADDRESS,private_person=PERSON,private_phone=PHONE_NUMBER,private_url=URL,secret=SECRET"

echo "Installed $package_id in $package_dir"
