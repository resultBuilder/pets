import { spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";

const root = new URL("../", import.meta.url).pathname;
const appBinary = path.join(root, "build/CodexPets.app/Contents/MacOS/CodexPets");

if (process.platform !== "darwin") {
  console.log("macOS GUI smoke skipped: requires darwin");
  process.exit(0);
}

const tempRoot = await mkdtemp(path.join(os.tmpdir(), "codex-pets-macos-smoke-"));
const socketDir = path.join(tempRoot, "runtime");
const socketPath = path.join(socketDir, "pi-pet.sock");
const telemetryPath = path.join(tempRoot, "gui-smoke.jsonl");

let app;
try {
  await run("sh", ["macos/CodexPets/build.sh"], {
    cwd: root,
    env: {
      ...process.env,
      CODEX_PETS_SKIP_VENDOR_PETDEX: "1",
    },
  });

  app = spawn(appBinary, [], {
    cwd: root,
    env: {
      ...process.env,
      PI_PET_SOCKET_DIR: socketDir,
      CODEX_PETS_GUI_SMOKE_FILE: telemetryPath,
      CODEX_PETS_GUI_SMOKE_OPEN_BROWSER: "1",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const appOutput = collectOutput(app);
  app.once("exit", (code, signal) => {
    if (code !== null && code !== 0) {
      console.error(`CodexPets exited during smoke: ${code ?? signal}\n${appOutput()}`);
    }
  });

  await waitForTelemetry((line) => line.event === "launch");
  await waitForTelemetry(
    (line) =>
      line.event === "petdexBrowser" &&
      line.bootStarted === true &&
      line.bootLoaded === true &&
      Number(line.rowCount) >= 5 &&
      line.selectedName,
  );
  await waitForSocket(socketPath, () => app.exitCode !== null, () => appOutput());

  const running = await requestDaemon("session.upsert", {
    sessionId: "gui-smoke",
    cwd: "/tmp",
    title: "GUI smoke",
    status: "running",
    safeSummary: "agent running",
  });
  assertArraySnapshot(running.payload, "running response");
  await waitForTelemetry((line) => line.event === "snapshot" && line.attention === "running" && line.stateID === "running");

  const failed = await requestDaemon("session.upsert", {
    sessionId: "gui-smoke",
    cwd: "/tmp",
    title: "GUI smoke",
    status: "failed",
    safeSummary: "provider HTTP 500",
  });
  assertArraySnapshot(failed.payload, "failed response");
  await waitForTelemetry((line) => line.event === "snapshot" && line.attention === "failed" && line.stateID === "failed");

  await requestDaemon("session.upsert", {
    sessionId: "gui-smoke",
    cwd: "/tmp",
    title: "GUI smoke",
    status: "running",
    safeSummary: "agent running",
  });
  const approvalPromise = requestDaemon("approval.request", {
    approvalId: "gui-smoke:approval-1",
    sessionId: "gui-smoke",
    toolCallId: "tool-1",
    toolName: "bash",
    commandSummary: "git push origin main",
    risk: "medium",
    timeoutMillis: 30_000,
  });
  await waitForTelemetry(
    (line) => line.event === "snapshot" && line.attention === "approval_required" && line.stateID === "waiting",
  );
  await requestDaemon("approval.respond", {
    approvalId: "gui-smoke:approval-1",
    decision: "approved",
    reason: "macOS GUI smoke",
  });
  const approval = await approvalPromise;
  if (approval.payload?.decision !== "approved") {
    throw new Error(`approval decision = ${JSON.stringify(approval.payload)}`);
  }
  await waitForTelemetry((line) => line.event === "snapshot" && line.attention === "running");

  console.log(`macOS GUI smoke passed (${telemetryPath})`);
} finally {
  await stopProcess(app);
  await stopProcessByCommand(appBinary);
  await rm(tempRoot, { recursive: true, force: true });
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

async function stopProcess(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  try {
    await Promise.race([once(child, "exit"), delay(3_000)]);
  } finally {
    if (child.exitCode === null) child.kill("SIGKILL");
  }
}

async function stopProcessByCommand(commandPath) {
  let output = "";
  try {
    output = await run("ps", ["-axo", "pid=,command="], { cwd: root });
  } catch {
    return;
  }
  const pids = output
    .split(/\n/)
    .map((line) => {
      const trimmed = line.trim();
      const space = trimmed.indexOf(" ");
      if (space === -1) return null;
      const pid = Number(trimmed.slice(0, space));
      const command = trimmed.slice(space + 1);
      const isAppProcess = command === commandPath || command.startsWith(`${commandPath} `);
      return Number.isFinite(pid) && isAppProcess ? pid : null;
    })
    .filter((pid) => pid && pid !== process.pid);
  for (const pid of pids) {
    try {
      process.kill(pid, "SIGTERM");
    } catch {}
  }
  await delay(500);
  for (const pid of pids) {
    try {
      process.kill(pid, 0);
      process.kill(pid, "SIGKILL");
    } catch {}
  }
}

async function waitForSocket(filePath, exited, output) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (existsSync(filePath)) return;
    if (exited()) throw new Error(`CodexPets exited before in-app daemon socket was ready:\n${output()}`);
    await delay(100);
  }
  throw new Error(`CodexPets did not create in-app daemon socket ${filePath}:\n${output()}`);
}

async function requestDaemon(method, payload) {
  const id = `${method}-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const message = {
    version: 1,
    kind: "request",
    id,
    method,
    payload,
  };
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(socketPath);
    let buffer = "";
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new Error(`${method} timed out`));
    }, method === "approval.request" ? 35_000 : 10_000);

    socket.setEncoding("utf8");
    socket.once("connect", () => {
      socket.write(`${JSON.stringify(message)}\n`);
    });
    socket.on("data", (chunk) => {
      buffer += chunk;
      const newline = buffer.indexOf("\n");
      if (newline === -1) return;
      clearTimeout(timer);
      socket.end();
      try {
        const response = JSON.parse(buffer.slice(0, newline));
        if (response.error) reject(new Error(`${method}: ${response.error.message}`));
        else resolve(response);
      } catch (error) {
        reject(error);
      }
    });
    socket.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
}

async function waitForTelemetry(predicate) {
  const deadline = Date.now() + 15_000;
  let lastLines = [];
  while (Date.now() < deadline) {
    if (existsSync(telemetryPath)) {
      const text = await readFile(telemetryPath, "utf8");
      lastLines = text
        .split(/\n/)
        .filter(Boolean)
        .map((line) => JSON.parse(line));
      const match = lastLines.find(predicate);
      if (match) return match;
    }
    await delay(100);
  }
  throw new Error(`timed out waiting for GUI telemetry; saw:\n${JSON.stringify(lastLines, null, 2)}`);
}

function assertArraySnapshot(snapshot, label) {
  for (const key of ["sessions", "pendingApprovals", "installedPets"]) {
    if (!Array.isArray(snapshot?.[key])) {
      throw new Error(`${label}: ${key} is ${JSON.stringify(snapshot?.[key])}, want array`);
    }
  }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
