import { createReadStream } from "node:fs";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";
import { deflateSync } from "node:zlib";
import { spawn } from "node:child_process";
import { once } from "node:events";
import os from "node:os";

const root = new URL("../", import.meta.url);
const chromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const screenshotPath = "/tmp/codex-pets-browser-smoke.png";
const darkScreenshotPath = "/tmp/codex-pets-browser-smoke-dark.png";

const types = new Map([
  [".html", "text/html; charset=utf-8"],
  [".css", "text/css; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".webp", "image/webp"],
  [".png", "image/png"],
]);

const server = createStaticServer();
await listen(server, 0);
const port = server.address().port;
const profileDir = await mkdir(join(os.tmpdir(), `codex-pets-chrome-${process.pid}`), {
  recursive: true,
}).then(() => join(os.tmpdir(), `codex-pets-chrome-${process.pid}`));

let chrome;
try {
  const pageURL = `http://127.0.0.1:${port}/index.html`;
  chrome = await launchChrome(pageURL, profileDir);
  const page = await waitForPage(chrome.port, pageURL);
  const cdp = await connectCDP(page.webSocketDebuggerUrl);
  await cdp.send("Runtime.enable");
  await cdp.send("Page.enable");
  await waitForReadyState(cdp);

  const spriteURL = makePNGAtlasDataURL(8, 8);
  const result = await evaluateSmoke(cdp, spriteURL);
  assertSmokeResult(result);

  const screenshot = await cdp.send("Page.captureScreenshot", { format: "png" });
  if (!screenshot.data || screenshot.data.length < 1000) {
    throw new Error("browser smoke screenshot was unexpectedly small");
  }
  await writeFile(screenshotPath, Buffer.from(screenshot.data, "base64"));

  await cdp.send("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-color-scheme", value: "dark" }],
  });
  await delay(100);
  const theme = await evaluateTheme(cdp);
  assertDarkTheme(theme);

  const darkScreenshot = await cdp.send("Page.captureScreenshot", { format: "png" });
  if (!darkScreenshot.data || darkScreenshot.data.length < 1000) {
    throw new Error("browser smoke dark screenshot was unexpectedly small");
  }
  await writeFile(darkScreenshotPath, Buffer.from(darkScreenshot.data, "base64"));
  cdp.close();
  console.log(`browser smoke passed (${screenshotPath}, ${darkScreenshotPath})`);
} finally {
  if (chrome) await stopChrome(chrome.process);
  server.close();
  await rm(profileDir, { recursive: true, force: true });
}

function createStaticServer() {
  return createServer((request, response) => {
    const url = new URL(request.url || "/", `http://${request.headers.host}`);
    const cleanPath = normalize(decodeURIComponent(url.pathname)).replace(/^(\.\.[/\\])+/, "");
    const requested = cleanPath === "/" ? "index.html" : cleanPath.replace(/^[/\\]+/, "");
    const filePath = join(root.pathname, requested);

    response.setHeader("Cache-Control", "no-store");
    response.setHeader("Content-Type", types.get(extname(filePath)) || "application/octet-stream");
    createReadStream(filePath)
      .on("error", () => {
        response.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
        response.end("Not found");
      })
      .pipe(response);
  });
}

function listen(server, port) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", resolve);
  });
}

async function launchChrome(pageURL, profileDir) {
  const chrome = spawn(
    chromePath,
    [
      "--headless=new",
      "--disable-gpu",
      "--no-first-run",
      "--no-default-browser-check",
      "--remote-debugging-port=0",
      `--user-data-dir=${profileDir}`,
      pageURL,
    ],
    { stdio: ["ignore", "ignore", "pipe"] },
  );

  let stderr = "";
  const portPromise = new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`Chrome did not expose DevTools:\n${stderr}`)), 10_000);
    chrome.stderr.on("data", (chunk) => {
      stderr += chunk.toString("utf8");
      const match = stderr.match(/DevTools listening on ws:\/\/127\.0\.0\.1:(\d+)\//);
      if (match) {
        clearTimeout(timeout);
        resolve(Number(match[1]));
      }
    });
    chrome.once("exit", (code, signal) => {
      clearTimeout(timeout);
      reject(new Error(`Chrome exited before DevTools was ready: ${code ?? signal}\n${stderr}`));
    });
  });

  return { process: chrome, port: await portPromise };
}

async function waitForPage(port, pageURL) {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const pages = await fetch(`http://127.0.0.1:${port}/json/list`).then((response) => response.json());
    const page = pages.find((item) => item.type === "page" && item.url === pageURL);
    if (page?.webSocketDebuggerUrl) return page;
    await delay(100);
  }
  throw new Error("Chrome page target was not found");
}

async function connectCDP(webSocketURL) {
  const socket = new WebSocket(webSocketURL);
  await once(socket, "open");

  let nextID = 0;
  const pending = new Map();
  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (!message.id) return;
    const callbacks = pending.get(message.id);
    if (!callbacks) return;
    pending.delete(message.id);
    if (message.error) callbacks.reject(new Error(message.error.message));
    else callbacks.resolve(message.result || {});
  });
  return {
    send(method, params = {}) {
      const id = ++nextID;
      socket.send(JSON.stringify({ id, method, params }));
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
      });
    },
    close() {
      socket.close();
    },
  };
}

async function waitForReadyState(cdp) {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const result = await cdp.send("Runtime.evaluate", {
      expression: "document.readyState",
      returnByValue: true,
    });
    if (result.result?.value === "complete") return;
    await delay(100);
  }
  throw new Error("browser page did not finish loading");
}

async function evaluateSmoke(cdp, spriteURL) {
  const source = `
    (async (spriteURL) => {
      const nextFrame = () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const waitFor = async (predicate) => {
        const deadline = Date.now() + 5000;
        while (Date.now() < deadline) {
          if (predicate()) return;
          await nextFrame();
        }
        throw new Error("condition timed out");
      };
      window.__nativeMessages = [];
      window.CodexPetsNative = {
        postMessage(payload) {
          window.__nativeMessages.push(payload);
        }
      };
      window.dispatchEvent(new CustomEvent("codex-pets-native-ready"));
      window.dispatchEvent(new CustomEvent("codex-pets-native-browser-pets", {
        detail: {
          pets: [{
            source: "installed",
            slug: "smoke-pet",
            displayName: "Smoke Pet",
            description: "Installed smoke pet",
            kind: "fox",
            submittedBy: "Imported",
            tags: ["smoke"],
            spritesheetUrl: spriteURL,
            nativePetId: "native-smoke-pet",
            frameWidth: 8,
            frameHeight: 8,
            canUninstall: true
          }, {
            source: "prebundled",
            slug: "smoke-catalog-pet",
            displayName: "Smoke Catalog Pet",
            description: "Bundled smoke pet",
            kind: "fox",
            submittedBy: "petdex",
            tags: ["smoke"],
            spritesheetUrl: spriteURL,
            nativePetId: "prebundled:smoke-catalog-pet:/tmp/catalog/smoke-catalog-pet",
            frameWidth: 8,
            frameHeight: 8,
            canUninstall: false
          }]
        }
      }));
      window.dispatchEvent(new CustomEvent("codex-pets-native-pi-install-status", {
        detail: {
          available: true,
          installed: false,
          needsUpdate: false,
          path: "/tmp/codex-pets.ts"
        }
      }));
      await waitFor(() => document.querySelector('.pet-row[data-source="installed"]'));

      document.querySelector('.pet-row[data-source="installed"]').click();
      await nextFrame();

      const installPiAction = document.querySelector("#install-pi-action");
      const uninstallPiAction = document.querySelector("#uninstall-pi-action");
      const piInstallEnabledBeforeClick = !installPiAction.disabled && installPiAction.textContent.trim() === "Install to Pi";
      installPiAction.click();
      await nextFrame();
      window.dispatchEvent(new CustomEvent("codex-pets-native-pi-install-result", {
        detail: {
          ok: true,
          message: "Installed Pi extension",
          available: true,
          installed: true,
          needsUpdate: false,
          path: "/tmp/codex-pets.ts"
        }
      }));
      await nextFrame();
      const piInstallHiddenAfterInstall = installPiAction.hidden || getComputedStyle(installPiAction).display === "none";
      const piStatusAfterInstall = document.querySelector("#pi-extension-status").textContent.trim();
      const piUninstallVisibleAfterInstall = !uninstallPiAction.hidden && getComputedStyle(uninstallPiAction).display !== "none";
      const piUninstallEnabledAfterInstall =
        !uninstallPiAction.disabled && uninstallPiAction.textContent.trim() === "Uninstall Pi";

      uninstallPiAction.click();
      await nextFrame();
      window.dispatchEvent(new CustomEvent("codex-pets-native-pi-uninstall-result", {
        detail: {
          ok: true,
          message: "Uninstalled Pi extension",
          available: true,
          installed: false,
          needsUpdate: false,
          path: "/tmp/codex-pets.ts"
        }
      }));
      await nextFrame();
      const piInstallVisibleAfterUninstall = !installPiAction.hidden && getComputedStyle(installPiAction).display !== "none";
      const piInstallEnabledAfterUninstall =
        !installPiAction.disabled && installPiAction.textContent.trim() === "Install to Pi";
      const piUninstallHiddenAfterUninstall =
        uninstallPiAction.hidden || getComputedStyle(uninstallPiAction).display === "none";
      const piStatusAfterUninstall = document.querySelector("#pi-extension-status").textContent.trim();

      const primaryAction = document.querySelector("#primary-action");
      const useButtonEnabledBeforeClick = !primaryAction.disabled && primaryAction.textContent.trim() === "Use";
      primaryAction.click();
      await nextFrame();

      const selectedFrame = document.querySelector("#sprite-preview .pet-sprite-frame");
      const selectedSprite = document.querySelector("#sprite-preview .pet-sprite");
      const selectedStyle = getComputedStyle(selectedSprite);

      return {
        nativeShell: document.documentElement.classList.contains("native-shell"),
        rowCount: document.querySelectorAll(".pet-row").length,
        selectedSource: document.querySelector("#source-label").textContent.trim(),
        selectedName: document.querySelector("#pet-name").textContent.trim(),
        primaryActionText: primaryAction.textContent.trim(),
        installPiActionText: installPiAction.textContent.trim(),
        piInstallEnabledBeforeClick,
        piInstallHiddenAfterInstall,
        piStatusAfterInstall,
        piUninstallVisibleAfterInstall,
        piUninstallEnabledAfterInstall,
        piInstallVisibleAfterUninstall,
        piInstallEnabledAfterUninstall,
        piUninstallHiddenAfterUninstall,
        piStatusAfterUninstall,
        useButtonEnabledBeforeClick,
        status: document.querySelector("#status").textContent.trim(),
        selectedBackground: selectedStyle.backgroundImage,
        selectedSpriteFrames: selectedSprite.style.getPropertyValue("--sprite-frames"),
        selectedFrameWidth: selectedFrame.style.getPropertyValue("--frame-w"),
        selectedFrameHeight: selectedFrame.style.getPropertyValue("--frame-h"),
        messages: window.__nativeMessages,
      };
    })(${JSON.stringify(spriteURL)})
  `;
  const result = await cdp.send("Runtime.evaluate", {
    expression: source,
    awaitPromise: true,
    returnByValue: true,
  });
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text || "browser smoke evaluation failed");
  }
  return result.result.value;
}

function assertSmokeResult(result) {
  const failures = [];
  if (!result.nativeShell) failures.push("native-shell class was not enabled");
  if (result.rowCount < 1) failures.push(`row count = ${result.rowCount}`);
  if (!result.selectedSource.startsWith("Installed")) failures.push(`selected source = ${result.selectedSource}`);
  if (result.selectedName !== "Smoke Pet") failures.push(`selected pet = ${result.selectedName}`);
  if (result.primaryActionText !== "Use") failures.push(`primary action = ${result.primaryActionText}`);
  if (!result.piInstallEnabledBeforeClick) failures.push("Install to Pi button was not enabled");
  if (!result.piInstallHiddenAfterInstall) failures.push("Install to Pi button stayed visible after install");
  if (result.piStatusAfterInstall !== "Pi extension installed") {
    failures.push(`Pi extension status after install = ${result.piStatusAfterInstall}`);
  }
  if (!result.piUninstallVisibleAfterInstall) failures.push("Uninstall Pi button was not visible after install");
  if (!result.piUninstallEnabledAfterInstall) failures.push("Uninstall Pi button was not enabled after install");
  if (!result.piInstallVisibleAfterUninstall) failures.push("Install to Pi button was not visible after uninstall");
  if (!result.piInstallEnabledAfterUninstall) failures.push("Install to Pi button was not enabled after uninstall");
  if (!result.piUninstallHiddenAfterUninstall) failures.push("Uninstall Pi button stayed visible after uninstall");
  if (result.piStatusAfterUninstall !== "Pi extension not installed") {
    failures.push(`Pi extension status after uninstall = ${result.piStatusAfterUninstall}`);
  }
  if (!result.useButtonEnabledBeforeClick) failures.push("Use button was not enabled for installed pet");
  if (!result.status) failures.push("status was empty");
  if (!result.selectedBackground.includes("data:image/png")) failures.push("selected sprite did not use data PNG spritesheet");
  if (result.selectedSpriteFrames !== "6") failures.push(`selected sprite frames = ${result.selectedSpriteFrames}`);
  if (result.selectedFrameWidth !== "8px" || result.selectedFrameHeight !== "8px") {
    failures.push(`selected frame size = ${result.selectedFrameWidth} x ${result.selectedFrameHeight}`);
  }
  const actions = result.messages.map((message) => message.action);
  for (const action of ["listBrowserPets", "getPiExtensionStatus", "installPiExtension", "uninstallPiExtension", "selectInstalledPet"]) {
    if (!actions.includes(action)) failures.push(`missing native message ${action}`);
  }
  const selection = result.messages.find((message) => message.action === "selectInstalledPet");
  if (selection?.petId !== "native-smoke-pet") {
    failures.push(`bad installed selection payload ${JSON.stringify(selection)}`);
  }
  if (failures.length > 0) {
    throw new Error(`browser smoke failed:\n- ${failures.join("\n- ")}`);
  }
}

async function evaluateTheme(cdp) {
  const source = `
    (() => {
      const color = (selector, property = "backgroundColor") =>
        getComputedStyle(document.querySelector(selector))[property];
      return {
        prefersDark: matchMedia("(prefers-color-scheme: dark)").matches,
        bodyBg: getComputedStyle(document.body).backgroundColor,
        sidebarBg: color(".source-pane"),
        paneBg: color(".detail-pane"),
        footerBg: color(".detail-footer"),
        text: getComputedStyle(document.querySelector("#pet-name")).color,
        secondaryText: getComputedStyle(document.querySelector("#pet-description")).color
      };
    })()
  `;
  const result = await cdp.send("Runtime.evaluate", {
    expression: source,
    returnByValue: true,
  });
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text || "browser theme evaluation failed");
  }
  return result.result.value;
}

function assertDarkTheme(theme) {
  const failures = [];
  if (!theme.prefersDark) failures.push("preferred color scheme was not dark");

  for (const [name, color] of Object.entries({
    bodyBg: theme.bodyBg,
    sidebarBg: theme.sidebarBg,
    paneBg: theme.paneBg,
    footerBg: theme.footerBg,
  })) {
    const rgb = parseRGB(color);
    if (!rgb || luminance(rgb) > 80) failures.push(`${name} remained light: ${color}`);
  }

  for (const [name, color] of Object.entries({
    text: theme.text,
    secondaryText: theme.secondaryText,
  })) {
    const rgb = parseRGB(color);
    if (!rgb || luminance(rgb) < 120) failures.push(`${name} was too dark: ${color}`);
  }

  if (failures.length > 0) {
    throw new Error(`browser dark theme failed:\n- ${failures.join("\n- ")}`);
  }
}

function parseRGB(value) {
  const match = String(value).match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3])];
}

function luminance([red, green, blue]) {
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

async function stopChrome(chrome) {
  if (chrome.exitCode !== null) return;
  chrome.kill("SIGTERM");
  await Promise.race([once(chrome, "exit"), delay(1000)]);
  if (chrome.exitCode === null) chrome.kill("SIGKILL");
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function makePNGAtlasDataURL(frameWidth, frameHeight) {
  const width = frameWidth * 8;
  const height = frameHeight * 9;
  const stride = width * 4 + 1;
  const raw = Buffer.alloc(stride * height);
  for (let y = 0; y < height; y += 1) {
    const row = Math.floor(y / frameHeight);
    raw[y * stride] = 0;
    for (let x = 0; x < width; x += 1) {
      const col = Math.floor(x / frameWidth);
      const offset = y * stride + 1 + x * 4;
      raw[offset] = row * 24;
      raw[offset + 1] = col * 28;
      raw[offset + 2] = 180;
      raw[offset + 3] = 255;
    }
  }
  const png = Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk("IHDR", Buffer.concat([uint32(width), uint32(height), Buffer.from([8, 6, 0, 0, 0])])),
    pngChunk("IDAT", deflateSync(raw)),
    pngChunk("IEND", Buffer.alloc(0)),
  ]);
  return `data:image/png;base64,${png.toString("base64")}`;
}

function pngChunk(type, data) {
  const name = Buffer.from(type);
  return Buffer.concat([uint32(data.length), name, data, uint32(crc32(Buffer.concat([name, data])))]);
}

function uint32(value) {
  const out = Buffer.alloc(4);
  out.writeUInt32BE(value >>> 0);
  return out;
}

function crc32(data) {
  let crc = 0xffffffff;
  for (const byte of data) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1;
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}
