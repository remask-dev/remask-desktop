# Remask

Remask 是一个本地优先的 AI PII 脱敏网关，由两个可独立构建、发布和部署的项目组成：

- `remask-core`：Go 实现的脱敏引擎、ONNX 推理服务和 HTTP/SSE AI API 网关。
- `remask-desktop`：Tauri 桌面客户端，用于管理 sidecar、模型、策略、上游服务和运行状态。

当前技术方案见 [docs/technical-design.md](docs/technical-design.md)。

