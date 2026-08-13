# remask-core

Go 实现的 PII 脱敏引擎与 HTTP JSON/SSE AI API 网关。

## Run

```bash
go run ./cmd/remask-core --addr 127.0.0.1:17680 --proxy-addr 127.0.0.1:17681
```

管理、模型与脱敏 API 监听 `--addr`；AI HTTP JSON/SSE 代理单独监听 `--proxy-addr`。代理支持三种入口：`/proxy/{service-id}/...` 精确选择服务；当 service ID 不存在时，`/proxy/{configured-domain}/...` 按已配置 `base_url` 的域名匹配；直接请求 `/*` 时按 HTTP 方法与请求适配方案自动匹配服务。域名入口不会转发到未配置主机；存在多个匹配服务时使用 service ID 排序后的第一个服务。

当 Upstream 已配置，但请求方法或 Path 没有命中所选请求适配方案时，网关会透明转发请求和响应，不执行脱敏或还原。审计日志将该请求标记为 `protection_mode: passthrough`，避免将未受保护的请求误认为已脱敏。未配置的 Upstream 仍返回 `404 UPSTREAM_NOT_FOUND`。

Upstream 配置、规则、审计设置和安全日志默认统一存放在用户 Home 的 `~/.remask` 隐藏目录，可通过 `--data-dir` 或 `REMASK_DATA_DIR` 指定。持久化数据统一使用 SQLite 数据库 `remask.db`，当前用于请求日志，后续可扩展其他本地数据；审计日志不会记录 Header、URL 查询参数或实体原文，只保存首尾保留的安全打码预览（如 `138***000`）、实际发送给上游的标签文本、实体标识与模型置信度。正则规则为确定性匹配，命中置信度固定为 `1.0`。普通规则使用 `<MASK_大写规则ID:四位码>`，模型继续使用 `<实体类型:四位码>`；实体类型开关只控制模型输出，普通规则由各自开关控制。

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

Core 使用 `machineid.ProtectedID("remask")` 获取应用隔离的设备标识，并在进程内通过 SHA-256 派生 256-bit 标签密钥。Remask 不持久化原始 Machine ID 或派生密钥；`~/.remask` 只保存配置和安全审计日志。相同设备上的相同实体会在独立请求中得到相同的 4 位伪随机标签，请求内反向映射会在代理响应结束后删除；网关不会使用 AI 提供商的响应 ID 建立会话状态。系统重装或系统 Machine ID 变化后，标签会随之变化。每个请求适配方案还可以声明 API Key Header 模板，托管凭证只需填写 API Key，网关按模板生成鉴权 Header。

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
./scripts/install-ai4privacy-distilbert.sh
```

目录示例：

```text
models/
├── openai-privacy-filter-q4/
│   ├── manifest.json
│   ├── model_q4.onnx
│   ├── model_q4.onnx_data
│   └── tokenizer.json
└── ai4privacy-distilbert-q4/
    ├── manifest.json
    ├── model_q4.onnx
    └── vocab.txt
```

从仓库目录执行 `go run ./cmd/remask-core` 时，如果存在默认模型，Core 会激活 `openai-privacy-filter-q4`，并自动查找相邻桌面项目资源目录中的 ONNX Runtime 动态库。也可以显式指定：

```bash
go build ./cmd/remask-core
./remask-core -models-dir ./models -active-model openai-privacy-filter-q4 -onnxruntime-lib /absolute/path/to/onnxruntime.dylib
```

Linux 使用 `libonnxruntime.so`，Windows 使用 `onnxruntime.dll`。原生库需要与当前 Go binding 使用的 ONNX Runtime C API 1.28 匹配。

不需要 ONNX 模型的精简服务端构建可使用 `-tags noonnxruntime`。

`openai-privacy-filter-q4` 使用约 907 MB 的 Q4 ONNX external-data 包、`o200k_base` tokenizer 与受约束 BIOES Viterbi 解码。`ai4privacy-distilbert-q4` 约 117 MB，适合对安装体积和内存更敏感的部署。两个模型均可通过模型管理 API 热切换。
