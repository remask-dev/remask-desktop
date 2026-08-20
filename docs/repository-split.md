# Repository split and binary supply chain

The project is split into three repositories:

* `remask-core` is private. It builds and tests the Go sidecar and publishes
  immutable platform archives.
* `remask-core-dist` is public. Its Git history contains release information,
  licenses, and notices; the large binary/model files are GitHub Release
  assets, not Git commits.
* `remask-desktop` is public. It contains the Tauri application and consumes a
  fixed Core release through `scripts/packaging.lock.json`.

The desktop workflow never checks out the private Core repository. `stage-core.sh`
calls `fetch-core.mjs`, which downloads `manifest.json`, verifies its SHA-256,
selects the target archive, verifies that archive, safely extracts it, and
stages the sidecar under Tauri's target-specific `binaries/` name. Built-in
models are treated as separate, platform-independent release assets and are
verified against both the Core release manifest and the desktop packaging lock.

## Local development

For an offline developer build, explicitly provide both local inputs:

```bash
REMASK_ALLOW_LOCAL_CORE=1 \
REMASK_CORE_BINARY=/absolute/path/remask-core \
REMASK_MODEL_ROOT=/absolute/path/models \
npm run stage:core
```

The override is rejected by formal builds unless `REMASK_ALLOW_LOCAL_CORE=1`
is set. Formal releases must pin `core.version` to a concrete version rather
than `latest`.

## Publishing Core

The private repository uses `.github/workflows/publish-core-binary.yml` to
build one archive per target, generate `manifest.json`, checksums, and model
assets, then upload a draft Release to `remask-core-dist`. The publish job
uses a GitHub App token with write access only to that distribution repository.
Set `CORE_DIST_APP_ID` and `CORE_DIST_APP_PRIVATE_KEY` as private repository
secrets and protect the `core-public-release` environment.

Release assets use this shape:

```text
remask-core-v1.3.0-aarch64-apple-darwin.tar.gz
openai-privacy-filter-q4f16-v7ffa9a043d54d1be65afb281eddf0ffbe629385b.tar.gz
manifest.json
SHA256SUMS
```

The Core binary reports its stamped semantic version and API version through
`GET /api/v1/version`. The release manifest and that runtime response must
agree before a desktop package is distributed.

Before making the split public, filter Git history into independent repositories
(`git filter-repo`), scan the resulting histories for secrets, add the desired
source/binary licenses, and rotate any credential that ever appeared in the
old monorepo.
