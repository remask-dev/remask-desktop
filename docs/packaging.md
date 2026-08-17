# Desktop packaging

All desktop packages use `remask-desktop/scripts/package.sh`. The script
selects a Rust target, rebuilds the ONNX-enabled Go sidecar, stages models and
the platform ONNX Runtime, runs Tauri, copies distributable files to
`artifacts/<platform>/<arch>/<version>/`, and writes `SHA256SUMS`.

## Standard commands

Run commands from `remask-desktop`:

```bash
npm ci

# Package for the current host platform.
npm run package:current

# Explicit platform commands.
npm run package:macos
npm run package:windows
npm run package:linux
```

`npm run desktop:build` remains an alias for `package:current`.

## ONNX Runtime inputs

Set one platform-specific path. `REMASK_ONNXRUNTIME_LIBRARY` remains available
as a generic override.

```bash
export REMASK_ONNXRUNTIME_MACOS_LIBRARY=/path/to/libonnxruntime.dylib
export REMASK_ONNXRUNTIME_WINDOWS_LIBRARY=/path/to/onnxruntime.dll
export REMASK_ONNXRUNTIME_LINUX_LIBRARY=/path/to/libonnxruntime.so
```

Provider companion libraries in the same directory are copied automatically.
The default bundled model is `openai-privacy-filter-q4f16`; set
`REMASK_MODEL_IDS` to a comma-separated list to override it.

## Platform requirements

### macOS

Build on macOS with Xcode Command Line Tools, Go, Rust, and Node.js installed.
The default output is a DMG for the host architecture.

### Windows

Native builds use `x86_64-pc-windows-msvc` and require Visual Studio Build
Tools plus Git Bash because the repository build scripts are Bash scripts. The
default output is an NSIS installer.

Windows x64 can also be cross-built from macOS or Linux. Install LLVM-MinGW
and NSIS, then set:

```bash
export REMASK_WINDOWS_TOOLCHAIN_DIR=/path/to/llvm-mingw
export REMASK_ONNXRUNTIME_WINDOWS_LIBRARY=/path/to/onnxruntime.dll
npm run package:windows
```

The cross-build uses Rust's `x86_64-pc-windows-gnullvm` target. If GitHub
release downloads require a mirror, Tauri supports:

```bash
export TAURI_BUNDLER_TOOLS_GITHUB_MIRROR_TEMPLATE='https://mirror.example/<owner>/<repo>/releases/download/<version>/<asset>'
```

The standard script statically links the LLVM-MinGW runtime so the installed
application does not require `libunwind.dll`. It also installs
`WebView2Loader.dll` beside `remask-desktop.exe` and fails the build if the
known runtime dependency checks do not pass.

### Linux

Build on a Linux host or Linux CI runner with the Tauri system dependencies,
Go, Rust, Node.js, and packaging tools installed. The defaults are DEB and
AppImage. Override them when required:

```bash
REMASK_LINUX_BUNDLES=deb,rpm,appimage npm run package:linux
```

## Architecture and signing

Use `REMASK_ARCH=x64` or `REMASK_ARCH=arm64`. A non-native architecture also
requires `REMASK_GO_CC` to point to a suitable cgo cross compiler. Developer
packages are unsigned by default. Set `REMASK_SIGN=1` only after configuring
the platform signing identity or Tauri signing command.
