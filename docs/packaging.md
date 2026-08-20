# Desktop packaging

All desktop packages use `remask-desktop/scripts/package.sh`. The script
selects a Rust target, downloads the locked Core binary and model assets from
the public Core distribution Release, verifies their checksums, stages the
platform ONNX Runtime, runs Tauri, copies distributable files to
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

These local commands may produce unsigned cross-build artifacts for
development. A release build must set `REMASK_RELEASE=1`, which rejects
non-native hosts and automatically requires code signing on macOS and Windows.
The Core version in `scripts/packaging.lock.json` must be a concrete version for
formal releases; `latest` is intended only for local development.
Setting `REMASK_SIGN=0` is rejected in release mode. The Windows release
workflow imports its signing certificate, builds on `windows-latest`, verifies
the Authenticode signature, silently installs the NSIS package, loads the
locked ONNX model through `remask-core -self-test`, starts the desktop
executable, and uploads the artifact only after all checks pass.

## Core and ONNX Runtime inputs

The desktop build does not compile or check out the private Core source. It
downloads `manifest.json` and the target archive from the public repository in
the `core.repository` lock entry. The manifest and target archive are verified
before extraction. Set `REMASK_CORE_VERSION` only for a local override of the
locked version; formal releases should update and commit the lock file.

For offline development, explicitly provide both local inputs:

```bash
REMASK_ALLOW_LOCAL_CORE=1 \
REMASK_CORE_BINARY=/absolute/path/remask-core \
REMASK_MODEL_ROOT=/absolute/path/models \
npm run stage:core
```

The local override is rejected by formal builds unless explicitly enabled.

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
packages are unsigned by default; set `REMASK_SIGN=1` to test a configured
signing identity without enabling release mode.

macOS release builds require `APPLE_SIGNING_IDENTITY` and notarization
credentials. Use either App Store Connect API credentials:

```bash
export APPLE_SIGNING_IDENTITY='Developer ID Application: Example (TEAMID)'
export APPLE_API_ISSUER='<issuer-id>'
export APPLE_API_KEY='<key-id>'
export APPLE_API_KEY_PATH='/secure/path/AuthKey_KEYID.p8'
REMASK_RELEASE=1 npm run package:macos
```

or Apple ID credentials by setting `APPLE_ID`, `APPLE_PASSWORD`, and
`APPLE_TEAM_ID`. After Tauri notarizes the application, the release command
also submits the final DMG, staples its ticket, validates it with Gatekeeper,
and only then generates the release checksum.

macOS release packaging re-signs bundled native libraries (including the ONNX
Runtime dylib) with the app's Developer ID identity before Tauri signs the app.
This keeps the Team ID consistent under Hardened Runtime library validation.

The GitHub macOS workflow expects `APPLE_CERTIFICATE` (Base64 PKCS#12),
`APPLE_CERTIFICATE_PASSWORD`, `APPLE_KEYCHAIN_PASSWORD`,
`APPLE_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_PASSWORD`, and `APPLE_TEAM_ID`
secrets.

### Formal local macOS release

The formal local entry point requires a clean worktree, a `v<version>` tag on
`HEAD`, a Developer ID Application identity in the login keychain, and Apple
notarization credentials:

```bash
export APPLE_ID='developer@example.com'
export APPLE_TEAM_ID='TEAMID1234'
export APPLE_SIGNING_IDENTITY='Developer ID Application: Example (TEAMID1234)'

# Store the app-specific password without writing it to a project file.
security add-generic-password -U \
  -a "$APPLE_ID" \
  -s remask-notary-password \
  -w

cd remask-desktop
npm run release:macos
```

The script reads the app-specific password from the macOS keychain, runs
`npm ci`, builds with `REMASK_RELEASE=1`, and verifies SHA-256, the DMG image,
Gatekeeper acceptance, and the stapled notarization ticket. For a local
release-candidate build before committing or tagging, use
`REMASK_ALLOW_DIRTY=1 REMASK_ALLOW_UNTAGGED=1`; those overrides should not be
used for the final published artifact.

Windows release builds require a code-signing certificate installed in the
current user's certificate store:

```bash
export REMASK_WINDOWS_CERTIFICATE_THUMBPRINT='<40-character-sha1-thumbprint>'
export REMASK_WINDOWS_TIMESTAMP_URL='http://timestamp.digicert.com' # optional
REMASK_RELEASE=1 npm run package:windows
```

The GitHub workflow expects `WINDOWS_CERTIFICATE` (Base64 PFX),
`WINDOWS_CERTIFICATE_PASSWORD`, and `WINDOWS_CERTIFICATE_THUMBPRINT` secrets.
It imports the PFX before building and rejects installers whose Authenticode
signature is not valid.

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
