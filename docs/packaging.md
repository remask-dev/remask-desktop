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

These local commands may produce cross-build artifacts for development. A
release build must set `REMASK_RELEASE=1`, which rejects non-native hosts. The
Windows release workflow builds on `windows-latest`, silently installs the
NSIS package, loads the locked ONNX model through `remask-core -self-test`,
starts the desktop executable, and uploads the artifact only after all checks
pass.

## ONNX Runtime inputs

The standard package command downloads the ONNX Runtime version and target
listed in `scripts/packaging.lock.json`, verifies its SHA-256, and never reuses
a previously staged runtime. A custom runtime must provide both its path and
its expected checksum. `REMASK_ONNXRUNTIME_LIBRARY` remains available as a
generic path override.

```bash
export REMASK_ONNXRUNTIME_MACOS_LIBRARY=/path/to/libonnxruntime.dylib
export REMASK_ONNXRUNTIME_WINDOWS_LIBRARY=/path/to/onnxruntime.dll
export REMASK_ONNXRUNTIME_LINUX_LIBRARY=/path/to/libonnxruntime.so
export REMASK_ONNXRUNTIME_SHA256=<expected-sha256>
```

Provider companion libraries in the same directory are copied automatically.
Every bundled model ID, revision, manifest, and file checksum must also be
listed in the packaging lock. The default is
`openai-privacy-filter-q4f16`; set `REMASK_MODEL_IDS` to a comma-separated list
of other locked models.

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
`WebView2Loader.dll` and the ONNX Runtime VC++ dependencies beside the app
executables, and fails the build if the known runtime dependency checks do not
pass. The x64 VC++ files are downloaded once from Microsoft and cached under
`remask-desktop/.cache`; set `REMASK_WINDOWS_VC_RUNTIME_DIR` to use a local
redistributable directory instead.

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

## Offline license public key

The Base64 Ed25519 verification key and its stable identifier are stored in
the private Go source and are compiled into the sidecar automatically. The
corresponding private key must remain in the license-issuing environment and
must never be made available to the desktop build.

The environment variables below are optional and are intended only for a
controlled public-key rotation build:

```bash
export REMASK_LICENSE_PUBLIC_KEY='<base64-ed25519-public-key>'
export REMASK_LICENSE_KEY_ID='prod-v1'
```

`REMASK_PURCHASE_URL` may be set while compiling the Tauri application to
replace the default purchase page. The desktop command appends only the fixed
product name and the validated Remask device ID.
