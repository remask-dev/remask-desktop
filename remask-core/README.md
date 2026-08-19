# remask-core

Go 实现的 PII 脱敏引擎与 HTTP JSON/SSE AI API 网关。

## Run

```bash
go run ./cmd/remask-core \
  --addr 127.0.0.1:17680 \
  --proxy-addr 127.0.0.1:17681 \
  --forward-proxy-addr 127.0.0.1:17682
```

管理、模型与脱敏 API 监听 `--addr`；AI HTTP JSON/SSE 反向代理监听 `--proxy-addr`；HTTP/HTTPS 与 SOCKS5 代理网关共用 `--forward-proxy-addr`。反向代理支持三种入口：`/proxy/{service-id}/...` 精确选择服务；当 service ID 不存在时，`/proxy/{configured-domain}/...` 按已配置 `base_url` 的域名匹配；直接请求 `/*` 时按 HTTP 方法与请求适配方案自动匹配服务。域名入口不会转发到未配置主机；存在多个匹配服务时使用 service ID 排序后的第一个服务。

当 Upstream 已配置，但请求方法或 Path 没有命中所选请求适配方案时，网关会检查 POST JSON 请求体；若存在 `model`、`messages`、`input` 或 `contents` 等模型请求特征，则使用合并 OpenAI、Anthropic 与 Gemini 字段的通用策略执行脱敏和响应还原。其他未知请求仍透明转发，并在审计日志中标记为 `protection_mode: passthrough`。直接入口无法按 Path 自动选路时，同类模型请求会兜底选择 service ID 排序后的第一个已配置 Upstream；未配置的 `/proxy/{service-id}/...` 仍返回 `404 UPSTREAM_NOT_FOUND`。

## 代理网关

代理网关可以直接用于支持 `HTTP_PROXY`/`HTTPS_PROXY` 或 SOCKS5 的现有程序，两种协议共用同一端口。建议 SOCKS 客户端使用 `socks5h://`，让域名在代理端解析并正确命中保护目标。新数据目录预置一条目标地址为 `*`、绑定 `generic` 适配方案的通用保护规则；可按需缩小或删除。Remask 根据保护目标决定是否解密 HTTPS 流量；未配置域名保持原始 TLS 字节隧道。已配置域名中未命中 Profile 的 Path、WebSocket、文件上传和其他未支持格式仍会透明转发，不会因无法识别而阻断。

首次启动会在数据目录生成本地 CA：

```text
~/.remask/certificates/remask-ca.pem
~/.remask/certificates/remask-ca-key.pem
```

私钥权限为 `0600`，不能复制到其他设备或交给第三方。CA 路径和 SHA-256 指纹可通过 `GET /api/v1/proxy/ca` 查询。

Claude Code 示例：

```bash
HTTPS_PROXY=http://127.0.0.1:17682 \
ALL_PROXY=socks5h://127.0.0.1:17682 \
NODE_EXTRA_CA_CERTS="$HOME/.remask/certificates/remask-ca.pem" \
claude
```

Codex CLI 示例：

```bash
HTTPS_PROXY=http://127.0.0.1:17682 \
ALL_PROXY=socks5h://127.0.0.1:17682 \
SSL_CERT_FILE="$HOME/.remask/certificates/remask-ca.pem" \
codex
```

新数据目录不预置 Upstream，API 网关提供商由用户按需添加。`generic` 请求适配方案合并 Claude Messages、OpenAI Chat Completions/Responses、Gemini GenerateContent 和 Codex HTTP/SSE 请求的字段级脱敏规则；原有的提供商专用方案继续保留，以兼容已有配置和托管凭证 Header。WebSocket 连接保持透明，但不进行消息级脱敏。

Upstream 配置、规则、审计设置和安全日志默认统一存放在用户 Home 的 `~/.remask` 隐藏目录，可通过 `--data-dir` 或 `REMASK_DATA_DIR` 指定。持久化数据统一使用 SQLite 数据库 `remask.db`，当前用于请求日志，后续可扩展其他本地数据；普通模式下审计日志不会记录 Header、URL 查询参数或实体原文，只保存首尾保留的安全打码预览（如 `138***000`）、实际发送给上游的标签文本、实体标识与模型置信度。正则规则为确定性匹配，命中置信度固定为 `1.0`。普通规则使用 `<MASK_大写规则ID:四位码>`，模型继续使用 `<实体类型:四位码>`；实体类型开关只控制模型输出，普通规则由各自开关控制。

## 离线授权

Core 从操作系统安装标识派生应用专用设备 ID，并在数据目录保存 `remask.license`。原始系统标识不会通过 API 返回或写入日志。授权文件使用 Ed25519 签名；客户端只包含公钥，私钥必须保存在独立的签发环境。当前授权状态仅用于设置页展示和导入，不限制 Core、API 网关或代理功能。

开发环境可以生成密钥和测试授权：

```bash
go run ./cmd/remask-license keygen --private-key /secure/path/remask-license-key.pem

go run ./cmd/remask-license issue \
  --private-key /secure/path/remask-license-key.pem \
  --key-id prod-v1 \
  --device-id RMK1-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX \
  --email customer@example.com \
  --valid-for 8760h \
  --output /tmp/remask.license
```

`keygen` 会将 Base64 公钥打印到标准输出。正式校验公钥保存在私有 Go 源码中并自动编译进 Core；`REMASK_LICENSE_PUBLIC_KEY` 和 `REMASK_LICENSE_KEY_ID` 仅用于受控的公钥轮换构建。源码或编译时内置的公钥优先且不能被运行时环境变量覆盖。任何私钥文件都不得放入仓库、安装包或构建日志。

设置中的 `record_raw_request` 默认关闭。开启后，请求日志详情会记录完整 URL、Header、原始请求体和返回体（包括已经还原给客户端的响应）；这些内容可能包含敏感信息，仅建议临时开启，并在排障后关闭。

实体识别默认使用 `patrickmn/go-cache` 进程内缓存，按输入文本的 SHA-256 摘要保存识别结果；缓存值只保留实体类型、位置和置信度等结构信息，不保存实体原文，命中时根据当前输入恢复。缓存命中会续期，过期项由后台清理。可通过 `PUT /api/v1/settings` 的 `audit.entity_cache_enabled` 和 `audit.entity_cache_ttl_seconds` 配置开关与过期时间（默认开启、900 秒，允许 1–86400 秒）。内存缓存不会写入磁盘。

缓存层通过统一接口隔离。多实例部署可直接切换到 Redis 6.2+：

```bash
REMASK_ENTITY_CACHE_BACKEND=redis \
REMASK_ENTITY_CACHE_REDIS_URL=redis://127.0.0.1:6379/0 \
./remask-core
```

可选的 `REMASK_ENTITY_CACHE_REDIS_PREFIX` 用于隔离环境，默认是 `remask:pii:entity:v1:`。Redis 后端使用 `GETEX` 原子完成读取和滑动续期；Redis 故障时缓存按 fail-open 处理，不会阻止本地脱敏检测。

常用管理接口：

```text
GET    /api/v1/audit/logs
DELETE /api/v1/audit/logs
GET    /api/v1/audit/stats?days=7
GET    /api/v1/settings
PUT    /api/v1/settings
GET    /api/v1/upstreams
POST   /api/v1/upstreams
PUT    /api/v1/upstreams/{id}
DELETE /api/v1/upstreams/{id}
```

Core 使用固定输入前缀的 HMAC 标签派生逻辑，不读取或持久化设备标识；相同实体会在独立请求、重启和不同设备上得到相同的 4 位伪随机标签。请求内反向映射会在代理响应结束后删除；网关不会使用 AI 提供商的响应 ID 建立会话状态。每个请求适配方案还可以声明 API Key Header 模板，托管凭证只需填写 API Key，网关按模板生成鉴权 Header。

## Test

```bash
go test ./...
```

针对已运行网关的 DeepSeek 黑盒自动化测试：

```bash
python3 ./scripts/test-deepseek-gateway.py
```

脚本默认测试 `http://127.0.0.1:17681`、管理端 `http://127.0.0.1:17680` 和
`deepseek-v4-flash`，覆盖用户内容脱敏、AI 回答敏感内容不脱敏、多轮对话及缓存未命中。
测试会读取安全审计日志验证实际发往上游的文本，因此需要启用请求日志、EMAIL 规则，并保持
`redact_ai_answers=false`。使用客户端透传凭证时通过环境变量传入：

```bash
REMASK_TEST_API_KEY=your-key python3 ./scripts/test-deepseek-gateway.py
```

地址、模型和 User-Agent 可通过 `--gateway-url`、`--management-url`、`--model`、
`--user-agent` 覆盖；默认 User-Agent 为 `remask-deepseek-gateway-e2e/1.0`。脚本不会修改或清空现有配置与日志。

若当前环境限制默认 Go build cache，可设置：

```bash
GOCACHE=/tmp/remask-go-cache go test ./...
```

## ONNX 模型运行时

默认构建包含 ONNX Runtime 支持。每个模型包固定存放在 `models/<model-id>`，目录名必须与 Manifest ID 一致。当前安装器支持：

```bash
./scripts/install-openai-privacy-filter.sh
```

也可以使用纯 Go 下载器自动从 Hugging Face 下载并生成带 SHA-256 校验的模型包。默认示例是 OpenAI Privacy Filter 的 `q4f16` 变体：

```bash
go run ./cmd/remask-model-downloader \
  -repo openai/privacy-filter \
  -variant q4f16 \
  -revision main \
  -output ./models
```

生成后可显式启用该包：

```bash
go run ./cmd/remask-core \
  -models-dir ./models \
  -builtin-models-dir ./bundled-models \
  -active-model openai-privacy-filter-q4f16 \
  -onnxruntime-lib /absolute/path/to/onnxruntime.dylib
```

Core merges the read-only built-in directory with the writable user model directory. A user model wins when both directories contain the same ID; built-in models can be activated but deletion is rejected.

ONNX Runtime 默认使用 `-onnx-provider auto`：macOS 优先 CoreML（Apple GPU/Neural Engine），Windows 优先 DirectML，Linux 优先 CUDA、ROCm 或 OpenVINO GPU；GPU provider 不可用时自动回退 CPU。也可以显式选择 provider 和 GPU 编号：

```bash
./remask-core -onnx-provider cuda -onnx-device 0
```

对应环境变量为 `REMASK_ONNX_PROVIDER` 和 `REMASK_ONNX_DEVICE`。显式指定 provider 时，如果动态库或驱动不支持该 provider，模型加载会返回错误；`auto` 模式会继续尝试其他 provider。GPU 版 ONNX Runtime 需要随包提供对应的 provider companion libraries 以及平台驱动/CUDA/ROCm/OpenVINO 运行库，单独替换 `onnxruntime.dll` 或 `libonnxruntime.so` 不足以启用 GPU。

下载器使用的模型地址模板为：

```text
https://huggingface.co/openai/privacy-filter/resolve/<revision>/onnx/model_q4f16.onnx
https://huggingface.co/openai/privacy-filter/resolve/<revision>/onnx/model_q4f16.onnx_data
https://huggingface.co/openai/privacy-filter/resolve/<revision>/tokenizer.json
https://huggingface.co/openai/privacy-filter/resolve/<revision>/config.json
https://huggingface.co/openai/privacy-filter/resolve/<revision>/tokenizer_config.json
https://huggingface.co/openai/privacy-filter/resolve/<revision>/viterbi_calibration.json
```

网络受限时可指定 Hugging Face 镜像，例如 `-base-url https://hf-mirror.com`，也可设置 `REMASK_HF_BASE_URL`。下载器会先读取仓库 API 的文件列表，只下载实际存在的 ONNX、tokenizer 和配置文件；外部数据文件（`.onnx_data`）以及 calibration 文件都是可选的，不会再因为仓库没有它们而报 404。下载器支持 `.part` 断点续传；私有仓库通过 `HF_TOKEN` 或 `-token` 传入访问令牌。

目录示例：

```text
models/
├── openai-privacy-filter-q4/
│   ├── manifest.json
│   ├── model_q4.onnx
│   ├── model_q4.onnx_data
│   └── tokenizer.json
```

从仓库目录执行 `go run ./cmd/remask-core` 时，如果存在默认模型，Core 会激活 `openai-privacy-filter-q4`，并自动查找相邻桌面项目资源目录中的 ONNX Runtime 动态库。也可以显式指定：

```bash
go build ./cmd/remask-core
./remask-core -models-dir ./models -active-model openai-privacy-filter-q4 -onnxruntime-lib /absolute/path/to/onnxruntime.dylib
```

Linux 使用 `libonnxruntime.so`，Windows 使用 `onnxruntime.dll`。原生库需要与当前 Go binding 使用的 ONNX Runtime C API 1.28 匹配。

不需要 ONNX 模型的精简服务端构建可使用 `-tags noonnxruntime`。

`openai-privacy-filter-q4` 使用约 907 MB 的 Q4 ONNX external-data 包、`o200k_base` tokenizer 与受约束 BIOES Viterbi 解码。模型可通过桌面端模型列表下载，也可调用 `POST /api/v1/models/download` 手动指定 Hugging Face 项目地址。Hugging Face 镜像在桌面端设置中配置，或通过下载请求的 `base_url` 指定。
