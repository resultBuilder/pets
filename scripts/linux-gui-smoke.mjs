import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const root = new URL("../", import.meta.url).pathname;
const appBinary = path.join(root, "build/linux/pi-pet-overlay");

if (process.platform !== "linux") {
  console.log("Linux GUI smoke skipped: requires linux");
  process.exit(0);
}

if (!(await commandOK("xvfb-run", ["--help"]))) {
  console.log("Linux GUI smoke skipped: xvfb-run was not found");
  process.exit(0);
}

if (!(await commandOK("pkg-config", ["--exists", "x11", "xext"]))) {
  console.log("Linux GUI smoke skipped: X11 development pkg-config files were not found");
  process.exit(0);
}

const hasWebKit41 = await commandOK("pkg-config", ["--exists", "gtk+-3.0", "webkit2gtk-4.1"]);
const hasWebKit40 = await commandOK("pkg-config", ["--exists", "gtk+-3.0", "webkit2gtk-4.0"]);
if (!hasWebKit41 && !hasWebKit40) {
  console.log("Linux GUI smoke skipped: GTK/WebKitGTK development pkg-config files were not found");
  process.exit(0);
}

const tempRoot = await mkdtemp(path.join(os.tmpdir(), "codex-pets-linux-smoke-"));
const socketDir = path.join(tempRoot, "runtime");
const telemetryPath = path.join(tempRoot, "gui-smoke.jsonl");

let app;
try {
  await run("sh", ["linux/build.sh"], { cwd: root, env: process.env });

  app = spawn(
    "xvfb-run",
    ["-a", appBinary, "-petdex-only"],
    {
      cwd: root,
      env: {
        ...process.env,
        PI_PET_SOCKET_DIR: socketDir,
        CODEX_PETS_GUI_SMOKE_FILE: telemetryPath,
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  const output = collectOutput(app);
  app.once("exit", (code, signal) => {
    if (code !== null && code !== 0) {
      console.error(`pi-pet-overlay exited during Linux GUI smoke: ${code ?? signal}\n${output()}`);
    }
  });

  await waitForTelemetry(
    (line) =>
      line.event === "petdexBrowser" &&
      line.bootStarted === true &&
      line.bootLoaded === true &&
      Number(line.rowCount) >= 1 &&
      line.selectedName,
    () => output(),
  );

  console.log(`Linux GUI smoke passed (${telemetryPath})`);
} finally {
  await stopProcess(app);
  await rm(tempRoot, { recursive: true, force: true });
}

async function commandOK(command, args) {
  try {
    await run(command, args, { cwd: root, env: process.env });
    return true;
  } catch {
    return false;
  }
}

function run(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    child.stdout.on("data", (chunk) => {
      output += chunk.toString("utf8");
    });
    child.stderr.on("data", (chunk) => {
      output += chunk.toString("utf8");
    });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) resolve(output);
      else reject(new Error(`${command} ${args.join(" ")} exited ${code ?? signal}\n${output}`));
    });
  });
}

function collectOutput(child) {
  let output = "";
  child.stdout?.on("data", (chunk) => {
    output += chunk.toString("utf8");
  });
  child.stderr?.on("data", (chunk) => {
    output += chunk.toString("utf8");
  });
  return () => output;
}

async function waitForTelemetry(predicate, output) {
  const deadline = Date.now() + 20_000;
  let last = [];
  while (Date.now() < deadline) {
    try {
      const text = await readFile(telemetryPath, "utf8");
      last = text
        .trim()
        .split(/\n+/)
        .filter(Boolean)
        .map((line) => JSON.parse(line));
      const hit = last.find(predicate);
      if (hit) return hit;
    } catch {
      // The app creates the telemetry file after WebKit starts.
    }
    await delay(100);
  }
  throw new Error(`timed out waiting for Linux GUI telemetry\nlast=${JSON.stringify(last)}\n${output()}`);
}

async function stopProcess(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  try {
    await Promise.race([once(child, "exit"), delay(3_000)]);
  } finally {
    if (child.exitCode === null) child.kill("SIGKILL");
  }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
