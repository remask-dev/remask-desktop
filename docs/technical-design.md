# Remask 技术方案

## 1. 文档状态
- 状态：首版实现中
- 核心项目：`remask-core`
- 桌面项目：`remask-desktop`

## 2. 背景与目标

Remask 在 AI 客户端和上游 AI API 之间提供本地 PII 保护：请求发出前识别并替换敏感实体，上游返回后再将合法标签还原为原文。

项目采用“独立客户端 + 独立核心”架构。首版以 Tauri 桌面应用托管 Go sidecar，后续同一个 Go 核心可以部署到服务器，不依赖桌面代码。

核心目标：

1. 使用确定性规则与 ONNX NER 模型共同识别 PII。
2. 支持模型加载、校验、热切换和回滚。
3. 代理主流 AI 的 HTTP JSON 与 SSE API。
4. 通过 Profile 描述协议字段，避免在 PII 核心中写死厂商协议。
5. 支持请求脱敏、响应还原以及跨 SSE 事件的完整标签识别。
6. 默认不持久化 PII 明文，不在日志和指标中暴露原始内容。
7. 将业务接入改动控制在最小范围：主流 SDK 默认只需修改 `base_url`，不要求修改请求体、流式处理代码或增加 Remask 专用 Header。

## 3. 非目标与首版边界

首版支持：

- HTTP/HTTPS 反向代理。
- 标准 HTTP 正向代理和 HTTPS `CONNECT`；仅对已配置 AI 域名使用本地 CA 解密。
- `application/json` 请求和非流式响应。
- `text/event-stream` SSE 响应。
- OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、Gemini GenerateContent 等内置 Profile。
- 使用相同配置机制添加 OpenAI-compatible 或自定义 JSON/SSE API。
- 文本检测、脱敏、还原的独立功能 API。
- 本地内存中的请求级或会话级映射。

首版不支持：

- WebSocket、gRPC、NDJSON 流式协议。
- 音频、视频和 multipart 内容处理。
- 对未知 JSON 中的所有字符串进行递归猜测。
- 对标签进行模糊匹配或容错还原。
- 多节点共享映射、多租户和集中式权限管理。

后续支持 NDJSON 时只新增传输编解码器，不修改 PII Pipeline。

上述未支持协议在正向代理模式下透明转发，不执行脱敏；“不支持处理”不等于“阻断连接”。审计和界面不得把透明转发请求标记为已保护。

## 4. 总体架构

```text
AI Client / remask-desktop
          |
          | base_url gateway or HTTP_PROXY / HTTPS_PROXY
          v
+------------------------- remask-core --------------------------+
| Authentication -> Router -> Upstream/Profile Matcher           |
| CONNECT Tunnel -> Selective TLS MITM -> Host Matcher            |
|                              |                                 |
|                       Document Transformer                     |
|                              |                                 |
|        Rules -> ONNX Detector -> Entity Merger -> Redactor     |
|                                               -> Scope Vault    |
|                              |                                 |
|                        Reverse Proxy -> Upstream AI             |
|                              |                                 |
|          JSON Transformer / SSE Decoder -> Stream Restorer     |
|                              |                                 |
|                         HTTP/SSE Response                       |
+----------------------------------------------------------------+
```

设计边界：

- PII Pipeline 只接收文本，不感知 HTTP、SSE 或 AI 厂商。
- Profile 只描述路由、文本字段和流事件字段，不实现实体识别。
- Proxy 只负责协议保持、超时、取消、Header 和流控。
- Tauri 不链接 Go 源码，只调用 `remask-core` 的版本化 HTTP API。
- 代理入口在单一端口下使用 `/proxy/{upstream}` 命名空间；转发前剥离该前缀，其余 path、query、method、Header、JSON 和 SSE 语义保持不变。

## 5. 项目结构

两个项目位于独立目录，拥有独立依赖、测试、版本和发布流程：

```text
remask/
├── remask-core/
│   ├── cmd/remask-core/
│   ├── internal/
│   │   ├── api/
│   │   ├── auth/
│   │   ├── config/
│   │   ├── gateway/
│   │   ├── profile/
│   │   ├── document/
│   │   ├── stream/sse/
│   │   ├── pii/
│   │   │   ├── detector/
│   │   │   ├── rules/
│   │   │   ├── onnx/
│   │   │   ├── merge/
│   │   │   ├── redact/
│   │   │   └── restore/
│   │   ├── scope/
│   │   ├── model/
│   │   ├── policy/
│   │   ├── upstream/
│   │   └── operation/
│   ├── profiles/
│   ├── schemas/
│   ├── models/
│   ├── tests/fixtures/
│   ├── go.mod
│   └── go.sum
├── remask-desktop/
│   ├── src/
│   ├── src-tauri/
│   ├── tests/
│   ├── package.json
│   └── Cargo.toml
├── docs/
└── README.md
```

`remask-core` 不引用 `remask-desktop` 的文件或运行时。桌面安装包可以携带对应平台的 `remask-core` 二进制和 ONNX Runtime 动态库，但它们仍是独立构建产物。

## 6. remask-core 模块设计

### 6.1 API 层

API 层负责：

- 路由和版本管理。
- 请求校验和统一错误响应。
- 数据面与控制面鉴权。
- 请求大小限制、超时和 request ID。
- 将 HTTP DTO 转换为领域对象。

API 层不包含规则识别、模型推理或厂商判断。

### 6.2 Gateway

Gateway 是通用反向代理，处理：

- 根据 `/proxy/{upstream}` 路由查找固定上游，不接受任意目标 URL。
- 根据上游绑定的 Profile 匹配 Operation。
- 修改已配置的请求 JSON 文本字段。
- 保留上游状态码和允许透传的 Header。
- 区分普通 JSON 响应与 SSE 响应。
- 客户端断开时取消上游请求和下游处理。
- 移除 hop-by-hop headers，修改 body 后重算 `Content-Length`。
- 默认透传调用方原有的上游认证 Header，使现有 SDK 只需修改 `base_url`。

Gateway 在单一监听端口提供数据面入口：

```text
/proxy/{upstream}/{original_path...}
```

`upstream` 是本地配置的稳定 ID。Gateway 用它选择固定上游和 Profile，然后在转发前剥离 `/proxy/{upstream}`，不会把 Remask 路由前缀发送给上游。

例如原地址：

```text
https://api.openai.com/v1
```

绑定 `openai` Upstream 后，本地兼容地址为：

```text
http://127.0.0.1:17680/proxy/openai/v1
```

客户端请求 `/proxy/openai/v1/responses` 时，Gateway 根据 `openai` 选择 Upstream，向上游发送 `/v1/responses`。Anthropic、Gemini 和其他上游复用同一个本地端口，只使用不同的 Upstream ID。

### 6.3 Profile

Profile 是厂商协议与核心之间的声明式适配层，描述：

- HTTP method 和 path 匹配条件。
- 请求中的文本字段。
- 普通响应中的文本字段。
- SSE data 编码方式、文本字段、通道字段和终止条件。

内置 Profile 与用户 Profile 走相同的解析、校验和执行逻辑。核心代码中禁止出现 `if provider == openai` 一类分支。

### 6.4 Document Transformer

首版仅处理 JSON。字段选择器采用受控 JSON Pointer Pattern：

```text
/messages/*/content
/messages/*/content/*/text
/choices/*/delta/content
```

支持对象键、数组下标和 `*` 通配符，不支持脚本表达式、递归下降或过滤器执行。选择到的值只有字符串才会转换，其他类型按 Profile 的严格级别选择忽略或报错。

### 6.5 PII Pipeline

处理顺序固定为：

```text
文本规范化
  -> 确定性规则检测
  -> ONNX NER 检测
  -> 实体规范化
  -> 重叠和冲突合并
  -> Policy 过滤
  -> 从右向左替换
  -> 脱敏文本与映射
```

输入文本本身不做破坏性 Unicode 归一化。需要规范化时必须同时保留到原始 UTF-8 byte offset 的映射。

### 6.6 Scope Vault

Vault 保存标签与原始实体的映射。首版为进程内存实现，分为：

- `request`：单次请求使用，响应完成或失败后删除。
- `session`：多轮远端会话使用，按 TTL 保存，需要显式传入 `scope_id`。

Vault API 不允许枚举或读取全部 PII 映射。还原操作只能提交待还原文本，避免管理客户端意外获取明文映射表。

### 6.7 Model Manager

Model Manager 负责：

- 扫描和校验模型包。
- 创建 ONNX Session。
- 运行 tokenizer 和模型自检。
- 预热新模型。
- 原子切换活动模型。
- 等待旧请求结束后释放旧 Session。
- 加载失败时保留当前活动模型。

耗时操作通过 Operation Manager 异步执行，API 返回 `operation_id`。

## 7. 标签与映射规范

标签格式固定为：

```text
<{ENTITY_TYPE}:{RANDOM_HEX_4}>
```

示例：

```text
<PHONE_NUMBER:A7F2>
<PERSON:91BC>
<EMAIL_ADDRESS:F0D3>
```

语法：

```regex
<[A-Z][A-Z0-9_]*:[A-F0-9]{4}>
```

生成规则：

1. 使用密码学安全随机源生成四位大写十六进制码。
2. 同一 Scope 内，相同实体类型和相同原文复用标签。
3. 同一 Scope 内，不同原文不得使用相同完整标签。
4. 标签已存在于输入原文或 Vault 时重新生成。
5. 随机码只负责降低碰撞和误匹配，不作为安全凭证。
6. 只还原当前 Scope 中精确存在的完整标签。
7. 不对被插入空格、改写大小写或缺失字符的标签做模糊还原。

实体类型由 Policy 规范化，例如：

```text
PERSON
PHONE_NUMBER
EMAIL_ADDRESS
ID_CARD
BANK_CARD
ADDRESS
IP_ADDRESS
ORGANIZATION
```

核心实体结构：

```go
type Entity struct {
    Type       string
    StartByte  int
    EndByte    int
    Text       string
    Confidence float32
    Sources    []string
    Priority   int
}
```

所有内部和 API 偏移统一使用原始 UTF-8 byte offset。

## 8. 实体检测与合并

### 8.1 确定性规则

适合规则处理的类型包括：

- 手机号、固定电话。
- 邮箱。
- IPv4/IPv6。
- 中国身份证号及校验位。
- 银行卡号及 Luhn 校验。
- 可明确识别的账号和业务编号。

规则结果不应仅依赖正则。存在校验算法的实体必须执行校验，减少误报。

### 8.2 ONNX NER

ONNX 主要识别人名、地址、组织、弱结构化证件描述等上下文实体。模型通过统一 Detector 接口接入：

```go
type Detector interface {
    ID() string
    Detect(ctx context.Context, text string) ([]Entity, error)
}
```

长文本采用 tokenizer 滑窗，窗口带重叠。模型 token offset 必须转换回原始 UTF-8 byte offset，再参与实体合并。

当前桌面默认内置一个模型；其他模型通过模型目录或 Hugging Face 项目地址下载后可切换：

- `openai-privacy-filter-q4`：固定 revision `7ffa9a043d54d1be65afb281eddf0ffbe629385b`，使用约 907 MB 的 ONNX Q4 external-data 包、`o200k_base` tokenizer、BIOES 标签和受约束 Viterbi 解码。桌面默认使用该模型。

当前实现包括滑动窗口、重叠预测合并、逐 token softmax 置信度和原始 UTF-8 byte offset 映射。推理后只接受显式支持的 PII 标签映射，未知标签直接丢弃，不能因为模型输出了任意类别就执行脱敏。

该模型当前主要用于英文人名、地址、组织等上下文实体。中文手机号、邮箱、身份证等结构化实体由确定性规则覆盖；中文姓名和地址的模型效果不作为首版能力承诺，后续可通过同一 Manifest 接口安装更合适的多语言模型。

### 8.3 冲突规则

默认合并策略：

1. 完全相同区间和类型时合并来源，采用更高置信度。
2. 通过校验算法的高精度规则优先于模型结果。
3. 同优先级下选择覆盖范围更完整的实体。
4. 部分重叠且类型不同的结果按照 Policy 优先级处理。
5. 合并后实体不得重叠，否则拒绝替换并返回内部诊断错误。

最终替换按 `StartByte` 从大到小执行，防止前序替换改变后续偏移。

## 9. ONNX 模型包

模型目录：

```text
models/
├── openai-privacy-filter-q4/
│   ├── model_q4.onnx
│   ├── model_q4.onnx_data
│   ├── tokenizer.json
│   ├── labels.json
│   ├── viterbi_calibration.json
│   └── manifest.json
```

Manifest 示例：

```json
{
  "schema_version": 1,
  "id": "openai-privacy-filter-q4",
  "name": "OpenAI Privacy Filter Q4",
  "version": "7ffa9a043d54d1be65afb281eddf0ffbe629385b",
  "task": "token-classification",
  "quantization": "q4",
  "label_scheme": "BIOES",
  "max_tokens": 512,
  "stride": 128,
  "files": {
    "model": {
      "path": "model_q4.onnx",
      "sha256": "8f7dee8b46d096f052b359375dfba5d983cc4d18c44a783bf548615c472f8dea",
      "size": 160219
    }
  },
  "tokenizer_config": { "type": "o200k-base" },
  "decoder": { "type": "viterbi-bioes", "operating_point": "default" },
  "minimum_confidence": {
    "*": 0.55
  }
}
```

模型包必须通过 schema、文件大小与 SHA-256、输入输出张量、标签集合和自检样例校验。当前 Go binding 使用 ONNX Runtime C API 1.28；桌面发行包必须携带受控版本的对应平台动态库。Q4 模型能否运行取决于模型实际使用的 ONNX 算子和目标平台的 ONNX Runtime 版本，不能只根据文件名判断兼容性。

桌面打包前由 `remask-desktop/scripts/stage-core.sh` 构建带 `onnxruntime` tag 的 sidecar，并将选定模型和动态库暂存到 Tauri resources。开发源码目录不复制模型，保持 `remask-core` 与 `remask-desktop` 的发布边界清晰。

核心支持通过 `--active-model` 或 `REMASK_ACTIVE_MODEL` 指定启动模型。指定后必须在开始监听 HTTP 前完成模型包校验、Session 创建和自检；任一步失败都终止启动，避免桌面显示核心在线但实际悄然退化为仅规则模式。桌面发行版默认传入随包模型 ID，服务端部署可以不设置该参数并通过管理 API 切换模型。

## 10. Profile 规范

示例：

```yaml
api_version: remask/v1alpha1
id: anthropic-messages
name: Anthropic Messages

operations:
  - id: create-message
    match:
      methods: [POST]
      paths: ["/v1/messages"]

    request:
      content_types: ["application/json"]
      text_fields:
        - "/system"
        - "/system/*/text"
        - "/messages/*/content"
        - "/messages/*/content/*/text"

    response:
      content_types: ["application/json"]
      text_fields:
        - "/content/*/text"

    stream:
      content_types: ["text/event-stream"]
      data_codec: json
      terminal:
        event_types: ["message_stop"]
      variants:
        - event_types: ["content_block_delta"]
          text_fields:
            - path: "/delta/text"
              channel:
                pointers: ["/index"]
```

Profile 匹配规则：

1. Upstream 必须绑定一个 Profile。
2. 同一 Profile 内按 method、path 和 content type 匹配 Operation。
3. 多个 Operation 同时匹配视为配置错误，不按顺序猜测。
4. 没有匹配时默认拒绝转换；可显式配置为原样透传。
5. 内置 Profile 只读，用户可以创建新版本覆盖 Upstream 绑定。

首版内置 Profile：

- `openai-chat-completions`
- `openai-responses`
- `anthropic-messages`
- `gemini-generate-content`
- `generic-openai-compatible`

这些只是预设配置，不是核心分支。

## 11. HTTP JSON 处理

### 11.1 请求

```text
客户端请求
  -> 鉴权和限制检查
  -> 查找 Upstream 和 Profile Operation
  -> 读取 JSON body
  -> 对 request.text_fields 执行脱敏
  -> 重新编码 JSON
  -> 设置正确 Content-Length
  -> 转发固定上游
```

首版允许 JSON 重新编码导致无意义的空格和对象键顺序变化，不允许改变值类型和数值语义。如果上游协议依赖请求 body 字节签名，则该 Operation 不支持正文转换。

### 11.2 普通响应

```text
上游 JSON 响应
  -> 检查 content type 和大小
  -> 对 response.text_fields 执行精确还原
  -> 重新编码 JSON
  -> 返回原状态码和安全 Header
```

非 JSON 错误响应默认原样透传，不扫描其中的字符串。

### 11.3 Header 策略

- 移除 `Connection`、`Keep-Alive`、`Proxy-*`、`TE`、`Trailer`、`Transfer-Encoding`、`Upgrade` 等 hop-by-hop headers。
- `passthrough` 模式按 Profile 允许列表转发调用方原有认证 Header；`managed` 模式由 Upstream 配置注入凭据，并禁止客户端覆盖受保护 Header。
- 不向上游转发本地控制 Token 和 `X-Remask-*` 内部 Header。
- 修改 body 后移除旧 `Content-Length`、`ETag` 和内容摘要 Header。
- `Content-Encoding: gzip` 的受保护 JSON 请求先解压并按解压后大小检查，脱敏后重新 gzip；无法识别的内容编码保持透传。
- 上游请求使用 `Accept-Encoding: identity`，不在压缩响应流中做转换。

## 12. SSE 处理

### 12.1 SSE 编解码

SSE Decoder 支持：

- `event:`、`data:`、`id:`、`retry:`。
- 多个 `data:` 行。
- 注释行。
- `\n` 和 `\r\n`。
- 空行结束事件。
- UTF-8 字节被底层读取拆分。
- 最大单行和最大事件大小限制。

SSE data 按 Profile 配置解析为 JSON。不能解析的事件按 Operation 的严格策略选择报错或原样透传。

### 12.2 跨事件标签还原

标签可能被上游拆分：

```text
event 1: "请联系 <PHONE_"
event 2: "NUMBER:A7F2> 获取信息"
```

每个逻辑输出通道维护独立扫描器：

```go
type StreamRestorer interface {
    Feed(channel string, delta string) string
    Flush(channel string) string
    FlushAll() map[string]string
}
```

扫描规则：

1. 普通文本立即输出。
2. 只有可能成为合法标签前缀的尾部进入缓冲区。
3. 收到完整 `>` 后，查询当前 Vault 并精确还原。
4. 完整标签不在 Vault 中时原样输出。
5. 缓冲内容不再可能构成合法标签时立即原样输出。
6. 超过最大标签长度仍未闭合时原样输出。
7. 流结束、终止事件、错误或客户端取消时执行 `FlushAll()`。

通道由 Profile 从事件 JSON 中生成，例如：

```text
choice:0
content-block:1
candidate:0/part:0
```

不同通道的标签片段绝不拼接。若某事件中的文本全部进入缓冲，允许输出空文本增量事件，以保持事件顺序和协议元数据。Profile 可以配置丢弃无语义的空增量，但首版默认保留事件。

### 12.3 背压与刷新

- 每处理完一个完整 SSE 事件后调用 HTTP Flusher。
- 不缓存完整响应。
- 单通道最多缓存一个合法标签的最大长度。
- 写入客户端失败立即取消上游请求。
- 上游读取、转换和客户端写入在同一请求上下文中受背压控制。

## 13. Scope 生命周期

### 13.1 默认代理请求

每次代理请求创建临时 Scope：

```text
创建 Scope -> 请求脱敏 -> 上游调用 -> 响应还原 -> 删除 Scope
```

无论成功、错误或取消，都必须在请求退出路径删除 Scope。

### 13.2 远端持久会话

如果上游保存会话历史，并在后续请求中可能输出过去收到的标签，Remask 应优先通过 Profile 声明的结构化字段自动关联远端会话和本地 Scope，而不是要求业务代码维护 Remask Header。

Profile 可以声明会话标识的提取位置：

```yaml
session:
  request:
    json_fields:
      - "/conversation_id"
      - "/previous_response_id"
    headers:
      - "X-Conversation-ID"
  response:
    json_fields:
      - "/conversation/id"
      - "/id"
    headers:
      - "X-Conversation-ID"
  stream:
    variants:
      - event_types: ["response.created"]
        id_fields:
          - "/response/id"
```

关联键使用：

```text
{upstream_id}:{credential_fingerprint}:{remote_session_id}
```

`credential_fingerprint` 是认证身份的不可逆摘要，不保存 API Key。该组合防止不同上游或不同账号的同名会话发生 Scope 串用。

自动关联流程：

1. 从请求的 Profile 字段提取远端会话 ID。
2. 找到已有绑定时复用对应 session Scope。
3. 未找到时创建新 Scope，并在响应出现会话 ID 后建立绑定。
4. 普通 JSON、响应 Header 或 SSE 事件中提取的新 ID 都绑定到当前 Scope。
5. `previous_response_id` 一类响应链 ID 可以多个指向同一 Scope。
6. Scope TTL 到期后删除其全部 Session Binding。

只有 Profile 明确声明的结构化字段可以用于自动关联。不得从模型自然语言正文中猜测会话 ID，也不得默认把所有响应 `id` 当作持久会话 ID。

显式 Scope Header 作为高级覆盖能力保留：

```http
X-Remask-Scope: scp_01J...
```

Scope 选择优先级：

```text
显式 X-Remask-Scope
  -> Profile 提取的远端会话绑定
  -> 新建临时 request Scope
```

普通无状态聊天接口不要求持久 Scope。由于下一轮请求通常会重新携带完整消息历史，Remask 可以针对该次请求重新检测并建立一致映射。只有上游在服务端保存上下文、后续请求不再携带完整历史时，才需要自动 Session Binding。

session Scope 设置最大 TTL 和最大映射数。到期后不再能够还原旧标签。桌面端需要展示会话映射的状态和到期时间，但不能显示映射明文。

## 14. remask-core API

API 分区：

```text
/api/v1/*                  功能和管理 API
/proxy/{upstream}/*        AI 网关数据面
/control/v1/*              本地桌面控制面
```

管理、控制和代理 API 共享一个监听端口，通过固定一级路径隔离。Upstream ID 禁止使用 `api`、`proxy`、`control` 等保留名称。

### 14.1 通用约定

- 请求和响应使用 UTF-8 JSON，代理 SSE 除外。
- 时间使用 RFC 3339 UTC。
- ID 使用带类型前缀的不可预测 ID，例如 `scp_...`、`op_...`。
- 写操作支持 `X-Request-ID`；异步操作返回 `operation_id`。
- API 不返回凭证明文和完整映射表。

统一错误格式：

```json
{
  "error": {
    "code": "PROFILE_NOT_MATCHED",
    "message": "no operation matched the request",
    "request_id": "req_01J...",
    "details": {}
  }
}
```

### 14.2 PII 功能 API

```text
POST   /api/v1/detect
POST   /api/v1/redact
POST   /api/v1/restore
POST   /api/v1/transform
GET    /api/v1/scopes/{scope_id}
DELETE /api/v1/scopes/{scope_id}
```

检测请求：

```json
{
  "text": "张三的手机号是 13800138000",
  "policy_id": "default"
}
```

检测响应：

```json
{
  "entities": [
    {
      "type": "PHONE_NUMBER",
      "text": "13800138000",
      "start_byte": 25,
      "end_byte": 36,
      "confidence": 1.0,
      "sources": ["rule", "onnx"]
    }
  ],
  "processing_ms": 12
}
```

脱敏请求：

```json
{
  "text": "张三的手机号是 13800138000",
  "policy_id": "default",
  "scope": {
    "mode": "request"
  }
}
```

脱敏响应：

```json
{
  "text": "<PERSON:91BC>的手机号是 <PHONE_NUMBER:A7F2>",
  "scope_id": "scp_01J...",
  "expires_at": "2026-08-12T10:10:00Z",
  "replacement_count": 2,
  "entities": [
    {
      "type": "PERSON",
      "start_byte": 0,
      "end_byte": 6,
      "confidence": 0.97,
      "sources": ["onnx"],
      "replacement": "<PERSON:91BC>"
    },
    {
      "type": "PHONE_NUMBER",
      "start_byte": 25,
      "end_byte": 36,
      "confidence": 1.0,
      "sources": ["rule", "onnx"],
      "replacement": "<PHONE_NUMBER:A7F2>"
    }
  ]
}
```

`redact` 必须在同一次响应中返回脱敏后的 `text` 和实际完成替换的 `entities`。实体顺序按原始输入中的 `start_byte` 升序排列，`start_byte` 和 `end_byte` 始终指向原始输入文本，而不是脱敏后的文本。每个实体的 `replacement` 必须与响应 `text` 中使用的标签一致。

默认不在 `redact.entities` 中重复返回实体原文，因为调用方已经持有输入文本，可以通过 byte offset 定位原文，同时避免响应、日志或调用链额外复制 PII。确有需要时，后续可以增加显式的 `include_original_text` 选项；该选项默认关闭，并受安全策略控制。

还原请求：

```json
{
  "scope_id": "scp_01J...",
  "text": "请联系 <PHONE_NUMBER:A7F2>"
}
```

还原响应：

```json
{
  "text": "请联系 13800138000",
  "restored_count": 1,
  "unknown_tokens": []
}
```

### 14.3 网关 API

默认代理入口：

```text
ANY /proxy/{upstream}/{original_path...}
```

同一监听端口示例：

```text
POST http://127.0.0.1:17680/proxy/openai/v1/responses
POST http://127.0.0.1:17680/proxy/anthropic/v1/messages
POST http://127.0.0.1:17680/proxy/gemini/v1beta/models/gemini-pro:streamGenerateContent
```

对应 SDK 通常只需修改一项配置：

```text
原 base_url: https://api.openai.com/v1
新 base_url: http://127.0.0.1:17680/proxy/openai/v1
```

API Key、模型名、请求 JSON、`stream` 参数和客户端的 SSE 消费方式保持不变。

可选高级 Header：

```text
X-Remask-Policy: default
X-Remask-Scope: scp_01J...
X-Remask-Request-ID: req_01J...
```

正常 SDK 接入不需要这些 Header。没有提供时，Gateway 使用 Upstream 的默认 Policy、自动会话关联和自动生成的 request ID。

Upstream 认证支持两种模式：

```text
passthrough   原样转发调用方的 Authorization、x-api-key 等 Profile 允许的认证 Header
managed       从 Secret Store 注入凭据，并拒绝调用方覆盖受保护 Header
```

`passthrough` 是代理入口的默认模式，可以做到只修改 `base_url`。`managed` 适合桌面端集中保存密钥，但代理请求必须额外携带 Remask 数据面 Token，防止其他本地进程滥用已保存凭据。

#### 14.3.1 接入兼容等级

首版按以下顺序保证兼容性：

1. **Level 1：只改 base URL**。保持 API Key、Header、path、query、JSON、超时、重试和 SSE 消费逻辑不变。这是主流 SDK 的默认目标。
2. **Level 2：base URL + 可选 Remask 配置**。仅在需要指定 Policy、显式 Scope 或 managed credential 时增加配置或 Header。
3. **Level 3：自定义 Profile**。请求仍通过标准 HTTP/SSE 发送，但需要为非内置协议声明文本字段和事件字段。

桌面端应为每个启用的 Upstream 直接生成并展示完整本地 base URL，例如 `http://127.0.0.1:17680/proxy/openai/v1`，同时提供连通性与 Profile 匹配测试。用户只需复制对应地址，不需要手工拼接路由。

不满足 Level 1 的典型情况：

- SDK 不允许修改 base URL。
- 客户端执行 TLS 证书固定。
- 上游要求基于原始 body 字节进行请求签名，而 Remask 需要修改 JSON body。
- 接口使用 WebSocket、gRPC、NDJSON 或 multipart。
- 文本位于首版 Profile 无法描述的二进制或加密字段。

这些情况必须使用专门适配或后续传输插件，不能通过猜测协议绕过。

### 14.4 模型 API

```text
GET  /api/v1/models
GET  /api/v1/models/{id}
GET  /api/v1/models/active
POST /api/v1/models/validate
POST /api/v1/models/{id}/activate
POST /api/v1/models/{id}/unload
```

激活模型返回 `202 Accepted`：

```json
{
  "operation_id": "op_01J...",
  "status": "pending"
}
```

### 14.5 Policy API

```text
GET    /api/v1/policies
POST   /api/v1/policies
GET    /api/v1/policies/{id}
PUT    /api/v1/policies/{id}
DELETE /api/v1/policies/{id}
POST   /api/v1/policies/{id}/test
```

Policy 控制启用的实体、Detector、置信度、冲突优先级、Scope TTL 和限制。

### 14.6 Profile API

```text
GET    /api/v1/profiles
POST   /api/v1/profiles
GET    /api/v1/profiles/{id}
PUT    /api/v1/profiles/{id}
DELETE /api/v1/profiles/{id}
POST   /api/v1/profiles/validate
POST   /api/v1/profiles/{id}/test
```

Profile test 只解析 fixture 并返回匹配字段，不访问真实上游。

### 14.7 Upstream API

```text
GET    /api/v1/upstreams
POST   /api/v1/upstreams
GET    /api/v1/upstreams/{id}
PUT    /api/v1/upstreams/{id}
DELETE /api/v1/upstreams/{id}
POST   /api/v1/upstreams/{id}/test
```

Upstream 示例：

```json
{
  "id": "anthropic",
  "alias": "anthropic",
  "base_url": "https://api.anthropic.com",
  "profile_id": "anthropic-messages",
  "credential": {
    "mode": "passthrough"
  },
  "timeouts": {
    "connect_ms": 10000,
    "request_ms": 300000
  }
}
```

### 14.8 Operation API

```text
GET    /api/v1/operations/{operation_id}
DELETE /api/v1/operations/{operation_id}
```

用于模型加载、模型校验等耗时任务的状态查询和取消。

### 14.9 系统与控制 API

```text
GET  /api/v1/health
GET  /api/v1/ready
GET  /api/v1/version
GET  /api/v1/capabilities
GET  /api/v1/metrics
GET  /control/v1/events
POST /control/v1/reload
POST /control/v1/shutdown
```

`/control/v1/events` 使用 SSE 向桌面端发送模型加载进度、配置错误和核心告警。`shutdown` 只在 desktop 模式启用。

## 15. Go 内部接口

PII：

```go
type EntityMerger interface {
    Merge(text string, candidates []Entity) ([]Entity, error)
}

type Redactor interface {
    Redact(text string, entities []Entity, vault Vault) (RedactResult, error)
}

type Restorer interface {
    Restore(text string, vault Vault) RestoreResult
}
```

Scope：

```go
type Vault interface {
    ID() string
    TokenFor(entityType, original string) (string, error)
    Resolve(token string) (string, bool)
    ExpiresAt() time.Time
}

type VaultStore interface {
    Create(ctx context.Context, ttl time.Duration) (Vault, error)
    Get(ctx context.Context, id string) (Vault, error)
    Delete(ctx context.Context, id string) error
}

type SessionBindingStore interface {
    Resolve(
        ctx context.Context,
        upstreamID string,
        credentialFingerprint string,
        remoteSessionID string,
    ) (scopeID string, found bool, err error)

    Bind(ctx context.Context, binding SessionBinding) error
    DeleteByScope(ctx context.Context, scopeID string) error
}
```

Profile 与文档：

```go
type ProfileRegistry interface {
    Match(profileID, method, path, contentType string) (Operation, error)
}

type DocumentTransformer interface {
    TransformJSON(
        body []byte,
        selectors []Selector,
        transform func(string) (string, error),
    ) ([]byte, error)
}
```

SSE：

```go
type SSEDecoder interface {
    Feed(data []byte) ([]SSEEvent, error)
    Flush() ([]SSEEvent, error)
}

type SSEEncoder interface {
    Encode(event SSEEvent) ([]byte, error)
}

type StreamTransformer interface {
    Transform(event SSEEvent) ([]SSEEvent, error)
    Flush() ([]SSEEvent, error)
}
```

模型：

```go
type ModelRuntime interface {
    Load(ctx context.Context, manifest ModelManifest) (ModelSession, error)
}

type ModelSession interface {
    Detect(ctx context.Context, text string) ([]Entity, error)
    Metadata() ModelMetadata
    Close() error
}

type ModelManager interface {
    Active() ModelSession
    Activate(ctx context.Context, modelID string) (OperationID, error)
}
```

## 16. remask-desktop 设计

桌面端职责：

- 安装、启动、健康检查和关闭 `remask-core` sidecar。
- 管理 Profile、Policy、Upstream 和模型。
- 提供独立脱敏测试台。
- 展示代理地址、运行状态、模型进度和不含 PII 的诊断信息。
- 将本地 CA 安装到当前用户的系统信任库，并展示证书指纹和安装状态。
- 通过白名单启动器为 Claude Code、Codex 等客户端注入进程级代理与 CA 环境。
- 通过系统安全存储管理上游凭据引用。

桌面端不承担：

- PII 检测和还原。
- SSE 内容转换。
- 模型推理。
- 保存映射明文。

### 16.1 Sidecar 生命周期

1. Tauri 生成本次启动的控制 Token。
2. 通过参数或受限临时文件启动 `remask-core --mode=desktop`。
3. Core 绑定 `127.0.0.1`，完成初始化后输出一次结构化 ready 消息。
4. Tauri 调用 `/api/v1/ready` 校验版本和能力。
5. 应用退出时调用 `/control/v1/shutdown`。
6. 超时未退出时由 Tauri 终止其启动的确切 PID。

当前桌面 sidecar 分别监听管理 API、反向网关和正向代理三个本机回环端口。端口必须互不冲突，桌面端基于实际端口生成 Upstream `base_url` 和正向代理地址。控制面和代理数据面使用不同 Token。

### 16.2 版本协商

Tauri 启动后调用 `/api/v1/version` 和 `/api/v1/capabilities`。桌面端声明支持的 API major version；major 不兼容时停止提供代理入口，并显示明确升级提示。

## 17. 配置与凭据

配置分为：

- 普通配置：Profile、Policy、Upstream 非敏感字段，使用结构化文件或本地数据库。
- 凭据：API Key、控制 Token，使用 Secret Store。
- 临时数据：Scope Vault、Operation 状态，默认仅内存。

定义 Secret Store 抽象：

- desktop 模式：系统 Keychain/Credential Manager/Secret Service。
- server 模式：环境变量、文件挂载或外部 Secret Manager。

Upstream 凭据模式：

- `passthrough`：不保存调用方密钥，认证 Header 随代理请求进入并只转发给绑定上游。
- `managed`：只保存 `credential_ref`，查询 API 永不返回明文。

无论哪种模式，日志、错误响应和 Session Binding 只能使用凭据的不可逆 fingerprint，不能保存或输出原始凭据。

## 18. 安全设计

### 18.1 网络与鉴权

- desktop 模式默认只监听 `127.0.0.1`，不监听局域网地址。
- 管理和控制请求始终需要对应 Token。
- 使用 managed credential 的代理请求需要数据面 Token。
- 使用 passthrough credential 的代理入口可以不增加 Remask Header，以实现只修改 `base_url`；它只能使用请求自身携带的上游凭据。
- 控制面 Token 不能用于代理；代理 Token 不能调用 shutdown。
- Upstream 使用允许列表，拒绝任意 URL，防止 SSRF 和开放代理。
- 禁止重定向到未授权域名，或在每次重定向后重新校验目标。
- 桌面模式应限制允许的 `Host`，并为浏览器来源请求实施严格 CORS/Origin 校验，降低网页利用 localhost 代理的风险。

### 18.2 数据保护

- 默认不落盘保存 PII 和 Vault。
- 日志不记录请求/响应 body、实体原文和完整认证 Header。
- 指标只记录实体类型、数量、延迟、错误码和字节数。
- panic、trace 和错误 details 在输出前执行敏感字段过滤。
- Scope 设置 TTL、最大实体数和最大内存占用。

### 18.3 资源限制

- JSON 请求和普通响应设最大 body 大小。
- SSE 设置最大行、事件、通道数和未闭合标签长度。
- ONNX 推理设置并发上限和上下文取消。
- Profile 数量、选择器数量和通配展开数量均有限制。
- 模型文件只能来自受管模型目录，API 不接受任意磁盘路径。

## 19. 可观测性

结构化日志字段：

```text
request_id
upstream_id
profile_id
operation_id
policy_id
transport
streaming
request_bytes
response_bytes
entity_counts_by_type
detector_latency_ms
upstream_latency_ms
total_latency_ms
error_code
```

禁止字段：

```text
request_body
response_body
original_entity_text
vault_mapping
authorization
api_key
```

`/api/v1/metrics` 首版可以返回 JSON 快照；服务端版本再增加 Prometheus 暴露方式。

## 20. 测试策略

### 20.1 单元测试

- Unicode byte offset 与 tokenizer offset 转换。
- 规则校验和误报样例。
- 实体重叠与合并优先级。
- 标签碰撞、复用和原文已有标签。
- 未知标签不还原。
- JSON Pointer Pattern 匹配。
- SSE 多行、CRLF、拆字节、异常结束。
- 标签跨两个或多个 SSE 事件。
- 多通道事件不得交叉拼接。

### 20.2 协议契约测试

每个内置 Profile 保存请求、普通响应和 SSE fixture。契约测试验证：

- 只修改目标字段。
- JSON 结构和非目标字段保持语义一致。
- SSE 事件类型、顺序、id 和终止事件保持一致。
- 标签被任意位置拆分后仍能还原。
- 未匹配 Operation 按配置拒绝或透传。

### 20.3 集成测试

使用本地 mock upstream 覆盖：

- HTTP 成功和错误响应。
- 慢响应、超时和客户端取消。
- SSE 正常结束、上游中断和超大事件。
- 模型切换时并发请求继续使用各自已获取的 Session。
- Scope TTL 到期与请求结束清理。

### 20.4 桌面端端到端测试

- sidecar 启动、版本协商和退出。
- 端口冲突处理。
- 模型加载进度事件。
- Upstream 和 Profile 配置生效。
- 应用异常退出后的 sidecar 回收策略。

## 21. 实施里程碑

### M1：规则引擎与非流式代理

- 建立两个独立项目骨架。
- 实现配置、鉴权、健康检查。
- 实现规则 Detector、实体合并、Scope Vault。
- 实现标签生成、脱敏和还原。
- 实现 JSON Pointer Pattern。
- 实现 HTTP JSON 代理和一个通用 Profile。

验收：规则引擎可以完成指定 JSON 字段的请求脱敏和响应还原。

### M2：SSE 与主流 Profile

- 实现完整 SSE 编解码器。
- 实现按通道的跨事件标签扫描器。
- 增加五个内置 Profile。
- 完成协议 fixture 契约测试。

验收：标签在任意字符位置跨 SSE 事件拆分时仍能精确还原，非目标字段不变。

### M3：ONNX 模型

- 实现模型 Manifest 和校验。
- 集成目标平台 ONNX Runtime。
- 实现 tokenizer、滑窗和 offset 映射。
- 实现模型异步加载、预热、热切换和回收。

验收：规则与模型实体能稳定合并；切换失败不影响旧模型和在途请求。

### M4：Tauri 桌面客户端

- sidecar 生命周期和版本协商。
- 模型、Policy、Profile、Upstream 管理。
- 脱敏测试台和状态事件。
- 系统凭据存储与跨平台打包。

验收：安装后无需外部运行时即可启动核心、配置代理并完成 HTTP/SSE 脱敏链路。

### M5：服务端准备

- 抽象持久化、Secret Store 和 Vault Store。
- 增加服务模式配置、TLS、服务鉴权和限流。
- 评估 Redis 或加密数据库 Scope 实现。

该阶段不改变 PII、Profile 和 SSE 核心接口。

## 22. 关键决策总结

1. `remask-core` 与 `remask-desktop` 是两个独立项目，仅通过 HTTP API 通信。
2. 首版传输范围固定为 HTTP JSON 和 SSE。
3. AI 协议通过声明式 Profile 适配，PII 核心不感知厂商。
4. 字段选择使用受控 JSON Pointer Pattern，不递归猜测所有字符串。
5. 标签固定为 `<TYPE:HEX4>`，例如 `<PHONE_NUMBER:A7F2>`。
6. 标签只在当前 Scope 内精确还原，不做模糊还原。
7. SSE 按逻辑通道维护小型尾部缓冲，支持标签跨事件拆分。
8. 模型采用 Manifest 管理并执行原子热切换。
9. 默认映射仅存内存，日志和指标不包含 PII 明文。
10. Go 核心通过存储和 Secret Provider 抽象为后续服务端部署保留扩展点。
11. Core 使用单一监听端口，通过 `/proxy/{upstream}` 路由多个上游；用户只修改 SDK `base_url`，认证方式、请求体和 HTTP/SSE 消费代码保持不变。
12. 上游结构化会话 ID 由 Profile 自动绑定到本地 Scope，`X-Remask-Scope` 仅作为高级覆盖能力。
