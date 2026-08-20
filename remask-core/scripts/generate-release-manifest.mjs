#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readFile, readdir, stat, writeFile } from "node:fs/promises";
import path from "node:path";

const [directory, version, outputPath = path.join(directory, "manifest.json")] = process.argv.slice(2);
if (!directory || !version) throw new Error("usage: generate-release-manifest.mjs <directory> <version> [output]");

async function sha256(filePath) {
  const hash = createHash("sha256");
  hash.update(await readFile(filePath));
  return hash.digest("hex");
}

const manifest = {
  schemaVersion: 1,
  coreVersion: version.replace(/^core-v/, "").replace(/^v/, ""),
  apiVersion: process.env.REMASK_CORE_API_VERSION || "v1",
  targets: {},
  models: {},
};
for (const entry of await readdir(directory)) {
  if (!entry.endsWith(".json") || entry === path.basename(outputPath)) continue;
  const input = JSON.parse(await readFile(path.join(directory, entry), "utf8"));
  if (input.target && input.asset) manifest.targets[input.target] = input;
  if (input.modelID && input.asset) manifest.models[input.modelID] = input;
}
await writeFile(outputPath, `${JSON.stringify(manifest, null, 2)}\n`);
console.log(`wrote ${outputPath}`);
