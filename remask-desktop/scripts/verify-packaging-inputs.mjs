import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import path from "node:path";

const [lockPath, runtimePath, runtimeSha256, modelsRoot, modelIDsValue] = process.argv.slice(2);
if (!lockPath || !runtimePath || !runtimeSha256 || !modelsRoot || !modelIDsValue) {
  throw new Error("usage: verify-packaging-inputs.mjs <lock> <runtime> <runtime-sha256> <models-root> <model-ids>");
}

const lock = JSON.parse(await readFile(lockPath, "utf8"));

async function sha256(filePath) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(filePath)) hash.update(chunk);
  return hash.digest("hex");
}

async function requireHash(filePath, expected) {
  const actual = await sha256(filePath);
  if (actual !== expected) {
    throw new Error(`checksum mismatch: ${filePath}\nexpected ${expected}\nactual   ${actual}`);
  }
}

if (runtimePath !== "-" && runtimeSha256 !== "-") {
  await requireHash(runtimePath, runtimeSha256);
}

for (const modelID of modelIDsValue.split(",").map(value => value.trim()).filter(Boolean)) {
  const modelLock = lock.models[modelID];
  if (!modelLock) throw new Error(`model is not locked for packaging: ${modelID}`);
  const modelDir = path.join(modelsRoot, modelID);
  const manifestPath = path.join(modelDir, "manifest.json");
  await requireHash(manifestPath, modelLock.manifestSha256);
  for (const [filename, expected] of Object.entries(modelLock.files)) {
    const filePath = path.join(modelDir, filename);
    const info = await stat(filePath);
    if (!info.isFile()) throw new Error(`model input is not a file: ${filePath}`);
    await requireHash(filePath, expected);
  }
}

console.log(`[package] verified locked packaging inputs: ${modelIDsValue}`);
