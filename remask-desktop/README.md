# remask-desktop

Tauri 2 desktop client for Remask. Its React frontend talks to the independent `remask-core` management API and controls the bundled Core sidecar in packaged builds.

## Frontend stack

- React 19 and strict TypeScript
- Vite 7
- Tailwind CSS 4 with theme tokens
- shadcn/ui source components backed by Radix UI
- TanStack Query for Core server state
- Lucide for the desktop icon system
- Tauri 2 commands for sidecar lifecycle

```bash
npm install
npm run dev
npm run typecheck
npm run build
```

## Development preview

The browser preview at `http://127.0.0.1:1420/` does not spawn a sidecar. Run `remask-core` separately and keep the default management URL `http://127.0.0.1:17680`, API gateway URL `http://127.0.0.1:17681`, and proxy gateway URLs `http://127.0.0.1:17682` / `socks5h://127.0.0.1:17682` in the app.

## System proxy integration

The desktop Settings view can install the generated Remask CA into the current user's operating-system trust store. macOS uses the login keychain, Windows uses the current-user Root store, and Linux uses `pkexec` plus `update-ca-certificates` when available. The confirmation dialog shows the CA SHA-256 fingerprint before installation; the CA private key never leaves `~/.remask`.

The same view provides quick-launch buttons for Claude Code and Codex. A new terminal starts with HTTP(S)_PROXY pointing to the HTTP endpoint, ALL_PROXY pointing to the SOCKS5 endpoint, and process-local CA variables already set. Quick launch does not add or enable protection rules and does not modify the user's shell profile or global proxy settings. On macOS, reusable launchers are written under `~/.remask/launchers` with mode `0700`.

## Desktop development and packaging

For local development, rebuild and stage the Go sidecar before Tauri starts so
source changes cannot silently run against an older Core binary:

```bash
# Rebuild Core and start the Tauri development app
npm run desktop:dev

# Package for the current host platform
npm run package:current

# Explicit platform entry points
npm run package:macos
npm run package:windows
npm run package:linux
```

After the first successful staging, these commands reuse the ONNX Runtime
already under `src-tauri/resources/onnxruntime`. Set
`REMASK_ONNXRUNTIME_LIBRARY` only when staging a runtime for the first time or
replacing it. `desktop:dev` keeps Vite frontend hot reload enabled but disables
Tauri's Rust watcher, which would otherwise interpret the freshly staged
sidecar as another source change and restart the app. Re-run the same command
after changing Go or Rust code.

Core stdout and stderr are written to `~/.remask/logs/core.log` as well as the
development terminal and can be followed with:

```bash
tail -f ~/.remask/logs/core.log
```

The packaging commands rebuild the ONNX-enabled Core, stage the model and
platform runtime, invoke Tauri, and copy distributable files plus SHA-256
checksums to `../artifacts/<platform>/<arch>/<version>/`.

Set the appropriate ONNX Runtime input before the first build:

```bash
REMASK_ONNXRUNTIME_MACOS_LIBRARY=/absolute/path/to/libonnxruntime.dylib npm run package:macos
REMASK_ONNXRUNTIME_WINDOWS_LIBRARY=/absolute/path/to/onnxruntime.dll npm run package:windows
REMASK_ONNXRUNTIME_LINUX_LIBRARY=/absolute/path/to/libonnxruntime.so npm run package:linux
```

Windows x64 cross-builds use LLVM-MinGW and Rust's
`x86_64-pc-windows-gnullvm` target; set `REMASK_WINDOWS_TOOLCHAIN_DIR` to the
LLVM-MinGW root. macOS packages must be built on macOS, and Linux packages on
Linux or a Linux CI runner. See [Desktop packaging](../docs/packaging.md) for
prerequisites, architecture overrides, bundle selection, and signing. By
default the staging script bundles `openai-privacy-filter-q4f16`; override it
through `REMASK_MODEL_IDS`.

The Core selects the platform GPU provider automatically. Override it with `REMASK_ONNX_PROVIDER` (`coreml`, `directml`, `cuda`, `tensorrt`, `rocm`, `openvino`, or `cpu`) and `REMASK_ONNX_DEVICE` when launching the desktop app or sidecar. The runtime bundle must include provider companion libraries for CUDA/ROCm/OpenVINO/DirectML; `stage:core` copies these files from the same directory as `REMASK_ONNXRUNTIME_LIBRARY`.

The bundled OpenAI package adds roughly 1 GB before installer compression. Additional models can be downloaded from the Models view after installation.

```bash
npm run stage:core
```

Development remains usable without staged resources and falls back to deterministic rules when a separately launched core was built without ONNX support.

The desktop client and standalone Core share `~/.remask`. Core uses the fixed HMAC input prefix and does not read or persist a machine identifier. The same entity keeps the same four-character pseudorandom tag across requests, restarts, and devices; request-local PII mappings are not persisted.

The proxy supports service-ID URLs (`/proxy/{service-id}/...`), configured-domain URLs (`/proxy/{upstream-domain}/...`), and direct protocol paths (`/*`). When multiple services match a domain or direct protocol path, Remask selects the first service by service ID.

The same hidden directory contains policy rules, upstream configuration, audit settings, and local persistent data in `remask.db`. The desktop UI provides a protection overview, per-entity redaction controls, request/entity statistics, masked audit inspection, upstream CRUD, model switching, connection settings, log retention, and local log cleanup.

## Frontend structure

The frontend is organized by feature. Core data remains in TanStack Query; React context only owns navigation, locale, and transient UI feedback.

```text
src/
├── main.tsx
├── app/                   # application shell and UI state
├── features/              # overview, logs, services, models, settings
├── shared/
│   ├── api/               # typed remask-core client
│   ├── i18n/              # locale provider and dictionaries
│   ├── lib/               # shadcn class composition utilities
│   └── ui/                # shadcn/Radix desktop controls
└── styles/                # Tailwind theme plus product-specific layouts
```

Simplified Chinese and English are available at runtime, and the selected locale is stored locally.
