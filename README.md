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

The browser preview at `http://127.0.0.1:1420/` does not spawn a sidecar. Run `remask-core` separately and keep the default management URL `http://127.0.0.1:17680` and proxy URL `http://127.0.0.1:17681` in the app.

## Stage the desktop sidecar

Before building a production Tauri bundle, stage the ONNX-enabled core, model package, and platform runtime:

```bash
REMASK_ONNXRUNTIME_LIBRARY=/absolute/path/to/libonnxruntime.dylib npm run stage:core
npm run tauri build
```

Set `TARGET_TRIPLE` when cross-compiling. By default the staging script bundles both `openai-privacy-filter-q4` and `ai4privacy-distilbert-q4`, with the OpenAI model active. Override this through comma-separated `REMASK_MODEL_IDS` and `REMASK_ACTIVE_MODEL`. The shell passes `--models-dir` and `--onnxruntime-lib`, then activates the selected bundled model before serving requests.

Bundling both current Q4 packages adds roughly 1 GB before installer compression. A smaller distribution can stage only AI4Privacy:

```bash
REMASK_MODEL_IDS=ai4privacy-distilbert-q4 \
REMASK_ACTIVE_MODEL=ai4privacy-distilbert-q4 \
REMASK_ONNXRUNTIME_LIBRARY=/absolute/path/to/libonnxruntime.dylib \
npm run stage:core
```

Development remains usable without staged resources and falls back to deterministic rules when a separately launched core was built without ONNX support.

The desktop client and standalone Core share `~/.remask`. Core derives its deterministic label key in memory from the application-scoped machine ID, so the same entity keeps the same four-character pseudorandom tag across launches without persisting a device key; request-local PII mappings are not persisted. Reinstalling the OS or changing the system machine ID changes these labels.

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
