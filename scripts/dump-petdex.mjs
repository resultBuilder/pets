#!/usr/bin/env node
import { mkdir, rm, stat, writeFile } from "node:fs/promises";
import { dirname, extname, join, posix } from "node:path";

const DEFAULT_MANIFEST = "https://assets.petdex.dev/manifests/petdex-v1.json";
const DEFAULT_ASSET_BASE = "https://assets.petdex.dev";
const FETCH_RETRIES = 4;
const RETRY_DELAY_MS = 1000;

class HTTPStatusError extends Error {
  constructor(url, status) {
    super(`${url} returned ${status}`);
    this.status = status;
  }
}

const options = parseArgs(process.argv.slice(2));

if (options.help) {
  printHelp();
  process.exit(0);
}

await dumpPetdex(options);

async function dumpPetdex({
  manifestURL,
  outDir,
  limit,
  concurrency,
  includePackages,
  allowPartial,
  resume,
}) {
  const startedAt = new Date().toISOString();
  console.log(`Loading ${manifestURL}`);
  const sourceManifest = await fetchJSON(manifestURL);
  const pets = normalizeManifest(sourceManifest, DEFAULT_ASSET_BASE);
  const selected = typeof limit === "number" ? pets.slice(0, limit) : pets;

  if (!resume) {
    await rm(outDir, { recursive: true, force: true });
  }
  await mkdir(outDir, { recursive: true });
  await writeJSON(join(outDir, "source-manifest.json"), sourceManifest);

  const failures = [];
  const localPets = [];
  let completed = 0;

  await mapLimit(selected, concurrency, async (pet) => {
    try {
      localPets.push(await dumpPet(outDir, pet, includePackages, resume));
      completed += 1;
      if (completed % 10 === 0 || completed === selected.length) {
        console.log(`Dumped ${completed}/${selected.length}`);
      }
    } catch (error) {
      failures.push({ slug: pet.slug, message: error.message || String(error) });
      if (!allowPartial) throw error;
    }
  });

  localPets.sort((a, b) => a.displayName.localeCompare(b.displayName));

  const remotePets = selected
    .map((pet) => ({
      source: "petdex",
      slug: pet.slug,
      displayName: pet.displayName,
      description: pet.description,
      kind: pet.kind,
      submittedBy: pet.submittedBy,
      tags: pet.tags,
      spritesheetUrl: pet.spritesheetUrl,
      petJsonUrl: pet.petJsonUrl,
      zipUrl: pet.zipUrl,
      frameWidth: pet.frameWidth,
      frameHeight: pet.frameHeight,
    }))
    .sort((a, b) => a.displayName.localeCompare(b.displayName));

  await writeJSON(join(outDir, "manifest.json"), {
    generatedAt: startedAt,
    sourceManifest: manifestURL,
    pets: localPets,
  });
  await writeJSON(join(outDir, "manifest.remote.json"), {
    generatedAt: startedAt,
    sourceManifest: manifestURL,
    pets: remotePets,
  });
  await writeJSON(join(outDir, "metadata.json"), {
    generatedAt: startedAt,
    sourceManifest: manifestURL,
    count: localPets.length,
    requestedCount: selected.length,
    includePackages,
    failures,
  });

  if (failures.length) {
    console.error(`Finished with ${failures.length} failed pet(s); see metadata.json`);
    process.exitCode = 1;
  } else {
    console.log(`Petdex dump written to ${outDir}`);
  }
}

async function dumpPet(outDir, pet, includePackages, resume) {
  const petDirName = safeSegment(pet.slug);
  const petDir = join(outDir, "pets", petDirName);
  await mkdir(petDir, { recursive: true });

  const spriteName = safeAssetName(pet.spritesheetUrl, "spritesheet.webp");
  const spritePath = join(petDir, spriteName);
  await downloadFile(pet.spritesheetUrl, spritePath, { resume });

  let localPetJsonURL = "";
  if (pet.petJsonUrl) {
    const petJsonPath = join(petDir, "pet.json");
    if (await downloadOptionalFile(pet.petJsonUrl, petJsonPath, { resume })) {
      localPetJsonURL = posix.join("pets", petDirName, "pet.json");
    }
  }

  let localZipURL = "";
  if (includePackages && pet.zipUrl) {
    const packageName = safeAssetName(pet.zipUrl, "package.zip");
    const packagePath = join(petDir, packageName);
    if (await downloadOptionalFile(pet.zipUrl, packagePath, { resume })) {
      localZipURL = posix.join("pets", petDirName, packageName);
    }
  }

  return {
    source: "petdex",
    slug: pet.slug,
    displayName: pet.displayName,
    description: pet.description,
    kind: pet.kind,
    submittedBy: pet.submittedBy,
    tags: pet.tags,
    spritesheetUrl: posix.join("pets", petDirName, spriteName),
    petJsonUrl: localPetJsonURL,
    zipUrl: localZipURL,
    remoteSpritesheetUrl: pet.spritesheetUrl,
    remotePetJsonUrl: pet.petJsonUrl,
    remoteZipUrl: pet.zipUrl,
    frameWidth: pet.frameWidth,
    frameHeight: pet.frameHeight,
  };
}

async function fetchJSON(url) {
  return withRetries(`GET ${url}`, async () => {
    const response = await fetch(url, { cache: "no-store" });
    if (!response.ok) throw httpError(url, response.status);
    return response.json();
  });
}

async function downloadFile(url, filePath, { resume = false } = {}) {
  if (resume && (await hasContent(filePath))) return;

  const data = await withRetries(`GET ${url}`, async () => {
    const response = await fetch(url);
    if (!response.ok) throw httpError(url, response.status);
    return Buffer.from(await response.arrayBuffer());
  });
  await mkdir(dirname(filePath), { recursive: true });
  await writeFile(filePath, data);
}

async function downloadOptionalFile(url, filePath, options) {
  try {
    await downloadFile(url, filePath, options);
    return true;
  } catch (error) {
    if (isPermanentHTTPError(error)) {
      console.warn(`Skipping optional asset ${url}: ${error.message}`);
      return false;
    }
    throw error;
  }
}

async function writeJSON(filePath, value) {
  await mkdir(dirname(filePath), { recursive: true });
  await writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`);
}

async function withRetries(label, operation) {
  let lastError;

  for (let attempt = 1; attempt <= FETCH_RETRIES; attempt += 1) {
    try {
      return await operation();
    } catch (error) {
      lastError = error;
      if (isPermanentHTTPError(error)) break;
      if (attempt === FETCH_RETRIES) break;
      const delay = RETRY_DELAY_MS * attempt;
      console.warn(
        `${label} failed (${error.message || String(error)}); retrying in ${delay}ms`,
      );
      await sleep(delay);
    }
  }

  throw lastError;
}

function httpError(url, status) {
  return new HTTPStatusError(url, status);
}

function isPermanentHTTPError(error) {
  return (
    error instanceof HTTPStatusError &&
    error.status >= 400 &&
    error.status < 500 &&
    error.status !== 408 &&
    error.status !== 429
  );
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function hasContent(filePath) {
  try {
    const info = await stat(filePath);
    return info.isFile() && info.size > 0;
  } catch {
    return false;
  }
}

function normalizeManifest(raw, defaultAssetBase) {
  if (raw?.v === 2 && Array.isArray(raw.pets)) {
    const base = trimSlash(raw.assetBase || defaultAssetBase);
    return raw.pets
      .map((item) => {
        if (!Array.isArray(item)) return null;
        const [slug, displayName, kind, submittedBy, spritesheet, petJson, zip] = item;
        return normalizePet({
          slug,
          displayName,
          kind,
          submittedBy,
          spritesheetUrl: spritesheet ? absolutizeAsset(spritesheet, base) : "",
          petJsonUrl: petJson ? absolutizeAsset(petJson, base) : "",
          zipUrl: zip ? absolutizeAsset(zip, base) : "",
        });
      })
      .filter(Boolean);
  }

  if (Array.isArray(raw?.pets)) {
    return raw.pets.map(normalizePet).filter(Boolean);
  }

  throw new Error("Unsupported Petdex manifest shape");
}

function normalizePet(input) {
  const slug = cleanString(input.slug || input.id);
  const displayName = cleanString(input.displayName || input.name || slug);
  const spritesheetUrl = cleanString(
    input.spritesheetUrl || input.spritesheetPath || input.spriteUrl,
  );
  if (!slug || !displayName || !spritesheetUrl) return null;

  return {
    slug,
    displayName,
    description: cleanString(input.description) || "Animated Codex pet from Petdex.",
    kind: cleanString(input.kind) || "pet",
    submittedBy: cleanString(input.submittedBy || input.author),
    tags: listStrings(input.tags || input.vibes),
    spritesheetUrl,
    petJsonUrl: cleanString(input.petJsonUrl),
    zipUrl: cleanString(input.zipUrl),
    frameWidth: positiveNumber(input.frameWidth) || 192,
    frameHeight: positiveNumber(input.frameHeight) || 208,
  };
}

async function mapLimit(items, limit, worker) {
  const queue = [...items];
  const workers = Array.from({ length: Math.min(limit, queue.length) }, async () => {
    while (queue.length) {
      const item = queue.shift();
      await worker(item);
    }
  });
  await Promise.all(workers);
}

function parseArgs(args) {
  const parsed = {
    manifestURL: DEFAULT_MANIFEST,
    outDir: "vendor/petdex",
    limit: null,
    concurrency: 6,
    includePackages: false,
    allowPartial: false,
    resume: false,
    help: false,
  };

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    switch (arg) {
      case "--manifest":
        parsed.manifestURL = requireValue(args, ++index, arg);
        break;
      case "--out":
        parsed.outDir = requireValue(args, ++index, arg);
        break;
      case "--limit":
        parsed.limit = positiveInteger(requireValue(args, ++index, arg), arg);
        break;
      case "--concurrency":
        parsed.concurrency = positiveInteger(requireValue(args, ++index, arg), arg);
        break;
      case "--include-packages":
        parsed.includePackages = true;
        break;
      case "--allow-partial":
        parsed.allowPartial = true;
        break;
      case "--resume":
        parsed.resume = true;
        break;
      case "--help":
      case "-h":
        parsed.help = true;
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }

  return parsed;
}

function requireValue(args, index, flag) {
  const value = args[index];
  if (!value || value.startsWith("--")) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function positiveInteger(value, flag) {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`${flag} must be a positive integer`);
  }
  return parsed;
}

function absolutizeAsset(value, base) {
  if (/^https?:\/\//i.test(value)) return value;
  return `${trimSlash(base)}/${String(value).replace(/^\/+/, "")}`;
}

function trimSlash(value) {
  return String(value).replace(/\/+$/, "");
}

function cleanString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function listStrings(value) {
  if (!Array.isArray(value)) return [];
  return value.map(cleanString).filter(Boolean).slice(0, 16);
}

function positiveNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : null;
}

function safeSegment(value) {
  return (
    String(value)
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 80) || "pet"
  );
}

function safeAssetName(url, fallback) {
  let name = fallback;
  try {
    const candidate = new URL(url).pathname.split("/").filter(Boolean).pop();
    if (candidate) name = candidate;
  } catch {}

  const cleaned = name.replace(/[^a-zA-Z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
  if (!cleaned) return fallback;
  if (extname(cleaned)) return cleaned;
  return `${cleaned}${extname(fallback)}`;
}

function printHelp() {
  console.log(`Usage: node scripts/dump-petdex.mjs [options]

Options:
  --manifest <url>       Petdex manifest URL (default: ${DEFAULT_MANIFEST})
  --out <dir>            Output directory (default: vendor/petdex)
  --limit <count>        Dump only the first N pets
  --concurrency <count>  Parallel downloads (default: 6)
  --include-packages     Also download package zip files when present
  --allow-partial        Keep pets that downloaded successfully if some fail
  --resume               Reuse existing non-empty downloaded files in the output directory
  --help                 Show this help
`);
}
