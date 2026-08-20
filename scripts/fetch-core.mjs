#!/usr/bin/env node

/*
 * Fetch the immutable Core and built-in model packages used by a desktop
 * build.  This script intentionally has no Go or private-repository
 * dependency: a public desktop fork can run it with only Node.js, curl-like
 * network access, and the archive utilities supplied by the host.
 *
 * Usage:
 *   node fetch-core.mjs <lock> <target-triple> <binary-dir> <models-dir> <model-ids>
 *
 * The normal source is a GitHub Release described by core.lock.json.  For
 * local/offline work, REMASK_CORE_BINARY and REMASK_MODEL_ROOT may point at
 * already downloaded inputs.  Those overrides are rejected for formal
 * releases unless REMASK_ALLOW_LOCAL_CORE=1 is explicitly set.
 */

import { createHash } from "node:crypto";
import { createReadStream, createWriteStream } from "node:fs";
import { readFile, readdir, mkdir, rm, cp, stat, rename } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const [lockPath, target, binaryDir, modelsDir, modelIDsValue] = process.argv.slice(2);
if (!lockPath || !target || !binaryDir || !modelsDir || modelIDsValue === undefined) {
  throw new Error(
    "usage: fetch-core.mjs <lock> <target-triple> <binary-dir> <models-dir> <model-ids>",
  );
}

const lock = JSON.parse(await readFile(lockPath, "utf8"));
const modelIDs = modelIDsValue
  .split(",")
  .map((value) => value.trim())
  .filter(Boolean);
const releaseMode = process.env.REMASK_RELEASE === "1";
const allowLocal = process.env.REMASK_ALLOW_LOCAL_CORE === "1";

function fail(message) {
  throw new Error(message);
}

function assertSha256(value, name) {
  if (!/^[a-f0-9]{64}$/i.test(value ?? "")) {
    fail(`${name} must be a 64-character SHA-256 hexadecimal digest`);
  }
}

function normalizeRepository(value) {
  if (!value) fail("core.repository is required in the packaging lock");
  if (/^https?:\/\//i.test(value)) return value.replace(/\/$/, "");
  if (/^[^/]+\/[^/]+$/.test(value)) return `https://github.com/${value}`;
  fail(`unsupported Core distribution repository: ${value}`);
}

function releaseTag(version, core) {
  if (core.tag) return core.tag;
  if (version === "latest") return "latest";
  return version.startsWith("v") ? version : `v${version}`;
}

function githubDownloadURL(repository, tag, asset) {
  const encodedAsset = asset.split("/").map(encodeURIComponent).join("/");
  if (tag === "latest") return `${repository}/releases/latest/download/${encodedAsset}`;
  return `${repository}/releases/download/${encodeURIComponent(tag)}/${encodedAsset}`;
}

async function sha256(filePath) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(filePath)) hash.update(chunk);
  return hash.digest("hex");
}

async function verifyHash(filePath, expected, label) {
  assertSha256(expected, `${label} sha256`);
  const actual = await sha256(filePath);
  if (actual.toLowerCase() !== expected.toLowerCase()) {
    fail(`checksum mismatch for ${label}\nexpected ${expected}\nactual   ${actual}`);
  }
}

async function ensureDirectory(directory) {
  await mkdir(directory, { recursive: true });
}

function isURL(value) {
  return /^https?:\/\//i.test(value) || /^file:\/\//i.test(value);
}

async function download(url, destination, label) {
  if (!isURL(url)) {
    await stat(url).catch(() => fail(`${label} does not exist: ${url}`));
    await cp(url, destination, { force: true });
    return;
  }
  const response = await fetch(url, {
    headers: { "User-Agent": "remask-desktop-packager/1.0", Accept: "application/octet-stream" },
    redirect: "follow",
  });
  if (!response.ok || !response.body) {
    fail(`download failed for ${label}: HTTP ${response.status} ${response.statusText}`);
  }
  const temporary = `${destination}.part`;
  await rm(temporary, { force: true });
  await ensureDirectory(path.dirname(destination));
  await pipeline(Readable.fromWeb(response.body), createWriteStream(temporary, { mode: 0o600 }));
  await rm(destination, { force: true });
  await rename(temporary, destination);
}

async function cachedAsset(url, cachePath, expected, label) {
  if (expected) {
    assertSha256(expected, `${label} sha256`);
    try {
      await verifyHash(cachePath, expected, label);
      return cachePath;
    } catch {
      // A stale or interrupted cache is replaced below.
    }
  } else {
    try {
      await stat(cachePath);
      return cachePath;
    } catch {
      // Download below.
    }
  }
  console.log(`[package] downloading ${label}`);
  await download(url, cachePath, label);
  if (expected) await verifyHash(cachePath, expected, label);
  return cachePath;
}

async function run(command, args, options = {}) {
  try {
    return await execFileAsync(command, args, { maxBuffer: 4 * 1024 * 1024, ...options });
  } catch (error) {
    const detail = error?.stderr || error?.stdout || error?.message || String(error);
    fail(`${command} ${args.join(" ")} failed: ${detail}`);
  }
}

function assertSafeEntry(entry) {
  const normalized = entry.replaceAll("\\", "/");
  if (
    normalized.startsWith("/") ||
    normalized.startsWith("../") ||
    normalized.includes("/../") ||
    normalized === ".." ||
    /^[A-Za-z]:\//.test(normalized)
  ) {
    fail(`archive contains an unsafe path: ${entry}`);
  }
}

async function listArchiveEntries(archive) {
  const lower = archive.toLowerCase();
  const tarLocal = process.platform === "win32" ? ["--force-local"] : [];
  if (lower.endsWith(".zip")) {
    const result = await run("unzip", ["-Z1", archive]);
    return result.stdout.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  }
  const args = lower.endsWith(".tar.gz") || lower.endsWith(".tgz")
    ? [...tarLocal, "-tzf", archive]
    : lower.endsWith(".tar.zst")
      ? [...tarLocal, "--zstd", "-tf", archive]
      : [...tarLocal, "-tf", archive];
  const result = await run("tar", args);
  return result.stdout.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

async function extractArchive(archive, destination) {
  const entries = await listArchiveEntries(archive);
  for (const entry of entries) assertSafeEntry(entry);
  await rm(destination, { recursive: true, force: true });
  await ensureDirectory(destination);
  const lower = archive.toLowerCase();
  if (lower.endsWith(".zip")) {
    await run("unzip", ["-q", archive, "-d", destination]);
  } else {
    const tarLocal = process.platform === "win32" ? ["--force-local"] : [];
    const args = lower.endsWith(".tar.gz") || lower.endsWith(".tgz")
      ? [...tarLocal, "-xzf", archive, "-C", destination]
      : lower.endsWith(".tar.zst")
        ? [...tarLocal, "--zstd", "-xf", archive, "-C", destination]
        : [...tarLocal, "-xf", archive, "-C", destination];
    await run("tar", args);
  }
}

async function walk(directory) {
  const result = [];
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch {
    return result;
  }
  for (const entry of entries) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...(await walk(full)));
    else if (entry.isFile()) result.push(full);
  }
  return result;
}

async function locateFile(root, requested, fallbackBasename) {
  if (requested) {
    const direct = path.join(root, requested);
    try {
      const info = await stat(direct);
      if (info.isFile()) return direct;
    } catch {
      // Archives often contain a single top-level directory. Search below.
    }
  }
  const candidates = (await walk(root)).filter((file) => path.basename(file) === fallbackBasename);
  if (candidates.length === 1) return candidates[0];
  if (candidates.length > 1) {
    // Prefer a path ending in the requested relative path when possible.
    const suffix = requested ? requested.replaceAll("\\", "/") : "";
    const preferred = candidates.find((file) => file.replaceAll("\\", "/").endsWith(`/${suffix}`));
    if (preferred) return preferred;
  }
  fail(`could not find ${requested || fallbackBasename} in extracted package`);
}

async function readReleaseManifest(core) {
  const version = process.env.REMASK_CORE_VERSION || core.version || "latest";
  if (releaseMode && version === "latest") {
    fail("formal desktop releases must pin core.version to a concrete version");
  }
  if (releaseMode) {
    assertSha256(core.manifestSha256, "core.manifestSha256 for a formal release");
  }
  const repository = normalizeRepository(process.env.REMASK_CORE_REPOSITORY || core.repository);
  const tag = releaseTag(version, core);
  const asset = core.manifestAsset || "manifest.json";
  const localRoot = process.env.REMASK_CORE_RELEASE_DIR;
  let source;
  if (localRoot) {
    source = path.join(localRoot, asset);
  } else if (core.manifestUrl) {
    source = core.manifestUrl.replaceAll("{version}", version).replaceAll("{tag}", tag);
  } else {
    source = githubDownloadURL(repository, tag, asset);
  }

  const cacheRoot = process.env.REMASK_PACKAGE_CACHE_DIR || path.resolve(path.dirname(lockPath), "../.cache");
  const cachePath = path.join(cacheRoot, "core", version, "manifest.json");
  let manifestPath;
  if (localRoot) {
    manifestPath = source;
  } else {
    manifestPath = await cachedAsset(source, cachePath, core.manifestSha256 || "", "Core release manifest");
  }
  if (core.manifestSha256) await verifyHash(manifestPath, core.manifestSha256, "Core release manifest");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest.schemaVersion !== 1) fail("unsupported Core release manifest schema");
  if (!manifest.coreVersion || typeof manifest.coreVersion !== "string") fail("Core release manifest has no coreVersion");
  if (version !== "latest" && manifest.coreVersion !== version && manifest.coreVersion !== version.replace(/^v/, "")) {
    fail(`Core manifest version ${manifest.coreVersion} does not match locked version ${version}`);
  }
  if (manifest.apiVersion && manifest.apiVersion !== (core.apiVersion || "v1")) {
    fail(`Core API version ${manifest.apiVersion} is incompatible with desktop lock`);
  }
  return { manifest, manifestPath, version: manifest.coreVersion, repository, tag };
}

async function stageCoreBinary(release, core, output) {
  const override = process.env.REMASK_CORE_BINARY;
  if (override) {
    if (releaseMode && !allowLocal) fail("REMASK_CORE_BINARY is not allowed for a formal release");
    await stat(override).catch(() => fail(`REMASK_CORE_BINARY does not exist: ${override}`));
    if (process.env.REMASK_CORE_SHA256) await verifyHash(override, process.env.REMASK_CORE_SHA256, "local Core binary");
    await ensureDirectory(output);
    const filename = target.includes("windows") ? `remask-core-${target}.exe` : `remask-core-${target}`;
    await cp(override, path.join(output, filename), { force: true });
    console.log(`[package] staged local Core binary ${filename}`);
    return { version: process.env.REMASK_CORE_VERSION || "local", asset: "local", sha256: await sha256(override) };
  }

  const releaseTarget = release.manifest.targets?.[target]
    ? target
    // Go sidecars are PE executables and do not link to the Tauri Rust CRT.
    // A public MSVC Core asset can therefore be consumed by the gnullvm Tauri
    // cross-build while still being staged under the requested target name.
    : target.endsWith("-pc-windows-gnullvm") && release.manifest.targets?.["x86_64-pc-windows-msvc"]
      ? "x86_64-pc-windows-msvc"
      : target;
  const targetSpec = release.manifest.targets?.[releaseTarget];
  if (!targetSpec || !targetSpec.asset || !targetSpec.sha256) {
    fail(`Core release manifest has no locked asset for target ${target}`);
  }
  assertSha256(targetSpec.sha256, `Core ${target} asset`);
  const localRoot = process.env.REMASK_CORE_RELEASE_DIR;
  const source = localRoot
    ? path.join(localRoot, targetSpec.asset)
    : githubDownloadURL(release.repository, release.tag, targetSpec.asset);
  const cacheRoot = process.env.REMASK_PACKAGE_CACHE_DIR || path.resolve(path.dirname(lockPath), "../.cache");
  const cachePath = path.join(cacheRoot, "core", release.version, target, path.basename(targetSpec.asset));
  const archive = localRoot ? source : await cachedAsset(source, cachePath, targetSpec.sha256, `Core ${target} package`);
  if (localRoot) await verifyHash(archive, targetSpec.sha256, `Core ${target} package`);
  const extraction = await fsTempDirectory(`remask-core-${target}-`);
  try {
    await extractArchive(archive, extraction);
    const binaryName = target.includes("windows") ? "remask-core.exe" : "remask-core";
    const binary = await locateFile(extraction, targetSpec.binary || `bin/${binaryName}`, binaryName);
    await ensureDirectory(output);
    const destination = path.join(output, target.includes("windows") ? `remask-core-${target}.exe` : `remask-core-${target}`);
    await cp(binary, destination, { force: true });
    await chmodExecutable(destination, target.includes("windows"));
    console.log(`[package] staged Core ${release.version} ${releaseTarget} as ${target} (${targetSpec.sha256})`);
    return { version: release.version, asset: targetSpec.asset, sha256: targetSpec.sha256 };
  } finally {
    await rm(extraction, { recursive: true, force: true });
  }
}

async function stageModels(release, modelIDs, output) {
  if (modelIDs.length === 0) return;
  const localRoot = process.env.REMASK_MODEL_ROOT;
  if (localRoot) {
    if (releaseMode && !allowLocal) fail("REMASK_MODEL_ROOT is not allowed for a formal release");
    for (const id of modelIDs) {
      const source = path.join(localRoot, id);
      await stat(source).catch(() => fail(`model package not found: ${source}`));
      await rm(path.join(output, id), { recursive: true, force: true });
      await cp(source, path.join(output, id), { recursive: true });
      console.log(`[package] staged local model ${id}`);
    }
    return;
  }

  for (const id of modelIDs) {
    const spec = release.manifest.models?.[id];
    if (!spec || !spec.asset || !spec.sha256) {
      fail(`Core release manifest has no model package for ${id}; publish it or set REMASK_MODEL_ROOT`);
    }
    assertSha256(spec.sha256, `model ${id} package`);
    const source = process.env.REMASK_CORE_RELEASE_DIR
      ? path.join(process.env.REMASK_CORE_RELEASE_DIR, spec.asset)
      : githubDownloadURL(release.repository, release.tag, spec.asset);
    const cacheRoot = process.env.REMASK_PACKAGE_CACHE_DIR || path.resolve(path.dirname(lockPath), "../.cache");
    const cachePath = path.join(cacheRoot, "core", release.version, "models", path.basename(spec.asset));
    const archive = process.env.REMASK_CORE_RELEASE_DIR
      ? source
      : await cachedAsset(source, cachePath, spec.sha256, `model ${id} package`);
    if (process.env.REMASK_CORE_RELEASE_DIR) await verifyHash(archive, spec.sha256, `model ${id} package`);
    const extraction = await fsTempDirectory(`remask-model-${id}-`);
    try {
      await extractArchive(archive, extraction);
      const manifest = await locateFile(extraction, spec.manifest || `${id}/manifest.json`, "manifest.json");
      const packageRoot = path.dirname(manifest);
      const parsed = JSON.parse(await readFile(manifest, "utf8"));
      if (parsed.id !== id) fail(`model manifest id ${parsed.id} does not match ${id}`);
      if (spec.manifestSha256) await verifyHash(manifest, spec.manifestSha256, `model ${id} release manifest`);
      const lockModel = lock.models?.[id];
      if (lockModel?.manifestSha256) await verifyHash(manifest, lockModel.manifestSha256, `model ${id} manifest`);
      if (lockModel?.files) {
        for (const [filename, expected] of Object.entries(lockModel.files)) {
          await verifyHash(path.join(packageRoot, filename), expected, `model ${id}/${filename}`);
        }
      }
      await rm(path.join(output, id), { recursive: true, force: true });
      await cp(packageRoot, path.join(output, id), { recursive: true });
      console.log(`[package] staged model ${id}`);
    } finally {
      await rm(extraction, { recursive: true, force: true });
    }
  }
}

async function fsTempDirectory(prefix) {
  const { mkdtemp } = await import("node:fs/promises");
  return mkdtemp(path.join(os.tmpdir(), prefix));
}

async function chmodExecutable(filePath, isWindows) {
  if (!isWindows) {
    const { chmod } = await import("node:fs/promises");
    await chmod(filePath, 0o755);
  }
}

const core = lock.core || {};
let release;
if (process.env.REMASK_CORE_BINARY && process.env.REMASK_MODEL_ROOT) {
  // Both inputs are explicitly supplied by the developer. Avoid requiring a
  // network manifest for any locally supplied input; formal releases still
  // reject the overrides unless the caller opts in above.
  release = {
    manifest: { targets: {}, models: {} },
    manifestPath: "local",
    version: process.env.REMASK_CORE_VERSION || "local",
    repository: "",
    tag: "",
  };
} else if (process.env.REMASK_CORE_BINARY && modelIDs.length === 0) {
  release = {
    manifest: { targets: {}, models: {} },
    manifestPath: "local",
    version: process.env.REMASK_CORE_VERSION || "local",
    repository: "",
    tag: "",
  };
} else {
  release = await readReleaseManifest(core);
}
await ensureDirectory(binaryDir);
await ensureDirectory(modelsDir);
const resolved = await stageCoreBinary(release, core, binaryDir);
await stageModels(release, modelIDs, modelsDir);
console.log(`[package] Core inputs ready version=${resolved.version} target=${target}`);
