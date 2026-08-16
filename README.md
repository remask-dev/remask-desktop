# Remask

Remask 是一个本地优先的 AI PII 脱敏网关，由两个可独立构建、发布和部署的项目组成：

- `remask-core`：Go 实现的脱敏引擎、ONNX 推理服务、HTTP/SSE AI API 网关和支持 HTTP/HTTPS、SOCKS5 的选择性解密代理网关。
- `remask-desktop`：Tauri 桌面客户端，用于管理 sidecar、模型、策略、上游服务和运行状态，并提供系统 CA 安装与 Claude Code/Codex 受保护快捷启动。

当前技术方案见 [docs/technical-design.md](docs/technical-design.md)。
