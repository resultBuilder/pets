const PREBUNDLED_MANIFEST_URL = "prebundled-pets/manifest.json";
const PREBUNDLED_SOURCE = "prebundled";
const LOCAL_MANIFEST_FETCH_TIMEOUT_MS = 2000;

const PREBUNDLED_PETS = [
  {
    slug: "boba",
    displayName: "Boba",
    description: "Animated Codex-compatible pet bundled with the browser.",
    kind: "creature",
    submittedBy: "railly",
    tags: ["prebundled"],
    spritesheetUrl: "pets/boba/spritesheet.webp",
    petJsonUrl: "pets/boba/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "cappy",
    displayName: "Cappy",
    description: "A cute coffee-loving capybara companion for calm focus sessions.",
    kind: "creature",
    submittedBy: "davidrichard1111",
    tags: ["prebundled"],
    spritesheetUrl: "pets/cappy/sprite.webp",
    petJsonUrl: "pets/cappy/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "pepe",
    displayName: "Pepe",
    description: "A compact Codex-style green frog pet in a plain blue shirt.",
    kind: "creature",
    submittedBy: "CodexPets",
    tags: ["prebundled"],
    spritesheetUrl: "pets/pepe/spritesheet.webp",
    petJsonUrl: "pets/pepe/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "pc-guy",
    displayName: "PC Guy",
    description: "Animated Codex-compatible pet bundled with the browser.",
    kind: "character",
    submittedBy: "c",
    tags: ["prebundled"],
    spritesheetUrl: "pets/pc-guy/sprite.webp",
    petJsonUrl: "pets/pc-guy/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "catfish",
    displayName: "Catfish",
    description: "A gray tabby cat-headed fish pet with a naturally fused fish body, rendered as a pixel-art Codex pet.",
    kind: "creature",
    submittedBy: "\u4e94\u5f00 \u4e94.",
    tags: ["prebundled"],
    spritesheetUrl: "pets/catfish/sprite.webp",
    petJsonUrl: "pets/catfish/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "paperclip",
    displayName: "Paperclip",
    description: "A tiny friendly office paperclip assistant for calm document-editing days.",
    kind: "object",
    submittedBy: "Vlad T.",
    tags: ["prebundled"],
    spritesheetUrl: "pets/paperclip/spritesheet.webp",
    petJsonUrl: "pets/paperclip/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
  {
    slug: "mallow",
    displayName: "Mallow",
    description: "A Codex digital pet based on the user cat: plush gray-and-white bicolor cat with gold eyes, pink nose, white bib and paws, gray mask, fluffy belly, and serious expression.",
    kind: "creature",
    submittedBy: "Aidil",
    tags: ["prebundled"],
    spritesheetUrl: "pets/mallow/spritesheet.webp",
    petJsonUrl: "pets/mallow/pet.json",
    frameWidth: 192,
    frameHeight: 208,
  },
];

const PERMISSION_PROFILES = {
  boba: {
    label: "Permissive",
    shortLabel: "No prompts",
    detail: "No Pi approval prompts",
  },
  "pc-guy": {
    label: "Strict",
    shortLabel: "Strict",
    detail: "Asks before every shell command and file edit",
  },
  default: {
    label: "Balanced",
    shortLabel: "Balanced",
    detail: "Asks before medium and high risk shell commands",
  },
};

const IDLE_STATE = {
  row: 0,
  frames: 6,
  durationMs: 1100,
};

const els = {
  search: document.querySelector("#search"),
  status: document.querySelector("#status"),
  list: document.querySelector("#pet-list"),
  spritePreview: document.querySelector("#sprite-preview"),
  sourceLabel: document.querySelector("#source-label"),
  petName: document.querySelector("#pet-name"),
  petDescription: document.querySelector("#pet-description"),
  permissionProfile: document.querySelector("#pet-permission-profile"),
  piExtensionStatus: document.querySelector("#pi-extension-status"),
  primaryAction: document.querySelector("#primary-action"),
  installPiAction: document.querySelector("#install-pi-action"),
  uninstallPiAction: document.querySelector("#uninstall-pi-action"),
  rowTemplate: document.querySelector("#pet-row-template"),
};

const state = {
  catalog: [],
  installed: [],
  selected: null,
  activePetSlug: "",
  query: "",
  nativeReady: false,
  busy: false,
  piInstallBusy: false,
  piUninstallBusy: false,
  piExtension: {
    available: false,
    installed: false,
    needsUpdate: false,
    path: "",
    sourceVersion: "",
    installedVersion: "",
  },
  lastMessage: "",
};

let previewKey = "";
let resizeFrame = 0;

window.__codexPetsAppBoot = {
  started: true,
  loaded: false,
  error: "",
};

try {
  init();
  window.__codexPetsAppBoot.loaded = true;
} catch (error) {
  window.__codexPetsAppBoot.error = error?.stack || error?.message || String(error);
  throw error;
}

function init() {
  bindEvents();
  installPrebundledCatalog();
  if (nativeBridgeAvailable()) {
    handleNativeReady();
  }
  loadBundledCatalog();
}

function bindEvents() {
  els.search.addEventListener("input", () => {
    state.query = els.search.value.trim().toLowerCase();
    state.lastMessage = "";
    renderList();
  });

  els.search.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      selectAdjacentPet(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      selectAdjacentPet(-1);
    } else if (event.key === "Enter" && state.selected) {
      event.preventDefault();
      useSelectedPet();
    }
  });

  els.primaryAction.addEventListener("click", useSelectedPet);
  els.installPiAction.addEventListener("click", installPiExtension);
  els.uninstallPiAction.addEventListener("click", uninstallPiExtension);

  window.addEventListener("codex-pets-native-ready", handleNativeReady);
  window.addEventListener("codex-pets-native-browser-pets", (event) => {
    const pets = Array.isArray(event.detail?.pets) ? event.detail.pets : [];
    const rows = pets.map(normalizePet).filter(Boolean);
    if (rows.length === 0) return;
    const selectedSlug = state.selected?.slug || "";
    const selectedWasCatalog = isCatalogPet(state.selected);
    state.installed = rows.filter((pet) => pet.source === "installed");
    state.catalog = rows.filter((pet) => pet.source !== "installed");
    state.nativeCatalog = true;
    if (selectedSlug && selectedWasCatalog) {
      const installedMatch = state.installed.find((pet) => pet.slug === selectedSlug);
      if (installedMatch) state.selected = installedMatch;
    }
    chooseInitialPet();
    render();
  });
  window.addEventListener("codex-pets-native-installed-pets", (event) => {
    const pets = Array.isArray(event.detail?.pets) ? event.detail.pets : [];
    const selectedSlug = state.selected?.slug || "";
    const selectedWasCatalog = isCatalogPet(state.selected);
    state.installed = pets.map(normalizePet).filter(Boolean);
    if (selectedSlug && selectedWasCatalog) {
      const installedMatch = state.installed.find((pet) => pet.slug === selectedSlug);
      if (installedMatch) state.selected = installedMatch;
    }
    chooseInitialPet();
    render();
  });
  window.addEventListener("codex-pets-native-import-result", (event) => {
    const detail = event.detail || {};
    state.busy = false;
    state.lastMessage = detail.message || (detail.ok ? "Done" : "Could not update pet");
    if (detail.ok && state.selected) {
      state.activePetSlug = state.selected.slug;
    }
    requestBrowserPets();
    render();
  });
  window.addEventListener("codex-pets-native-pi-install-result", (event) => {
    const detail = event.detail || {};
    state.piInstallBusy = false;
    state.lastMessage = detail.message || (detail.ok ? "Installed Pi extension" : "Could not install Pi extension");
    updatePiExtensionState(detail);
    requestPiExtensionStatus();
    render();
  });
  window.addEventListener("codex-pets-native-pi-uninstall-result", (event) => {
    const detail = event.detail || {};
    state.piUninstallBusy = false;
    state.lastMessage = detail.message || (detail.ok ? "Uninstalled Pi extension" : "Could not uninstall Pi extension");
    updatePiExtensionState(detail);
    requestPiExtensionStatus();
    render();
  });
  window.addEventListener("codex-pets-native-pi-install-status", (event) => {
    updatePiExtensionState(event.detail || {});
    render();
  });
}

async function loadBundledCatalog() {
  setStatus("Loading bundled pets...");
  try {
    const manifest = await fetchJSON(PREBUNDLED_MANIFEST_URL, LOCAL_MANIFEST_FETCH_TIMEOUT_MS);
    const pets = normalizeManifest(manifest, new URL(PREBUNDLED_MANIFEST_URL, document.baseURI));
    if (pets.length === 0) throw new Error("Manifest is empty");
    // In the native shell the daemon serves the merged catalog; the local
    // manifest is only the web fallback.
    if (state.nativeCatalog) return;
    state.catalog = pets;
    state.lastMessage = "";
  } catch {
    state.lastMessage =
      state.catalog.length > 0 ? "Using bundled pets" : "Bundled pets could not be loaded";
  }
  chooseInitialPet();
  render();
}

function installPrebundledCatalog() {
  const pets = normalizeManifest(
    { pets: PREBUNDLED_PETS },
    new URL(PREBUNDLED_MANIFEST_URL, document.baseURI),
  );
  state.catalog = pets;
  state.lastMessage = "";
  chooseInitialPet();
  render();
}

async function fetchJSON(url, timeoutMs) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(url, {
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return await response.json();
  } finally {
    clearTimeout(timeout);
  }
}

function normalizeManifest(raw, manifestURL = new URL(document.baseURI)) {
  if (raw?.v === 2 && Array.isArray(raw.pets)) {
    const base = trimSlash(raw.assetBase || new URL(".", manifestURL).href);
    return raw.pets
      .map((item) => {
        if (!Array.isArray(item)) return null;
        const [slug, displayName, kind, submittedBy, spritesheet, petJson, zip] = item;
        return normalizePet({
          source: PREBUNDLED_SOURCE,
          slug,
          displayName,
          kind,
          submittedBy,
          spritesheetUrl: absolutizeAsset(spritesheet, base),
          petJsonUrl: petJson ? absolutizeAsset(petJson, base) : "",
          zipUrl: zip ? absolutizeAsset(zip, base) : "",
        });
      })
      .filter(Boolean);
  }

  if (Array.isArray(raw?.pets)) {
    return raw.pets
      .map((pet) => normalizePet({ ...pet, source: PREBUNDLED_SOURCE }))
      .map((pet) => resolveManifestPetAssets(pet, manifestURL))
      .filter(Boolean);
  }

  throw new Error("Unsupported manifest shape");
}

function resolveManifestPetAssets(pet, manifestURL) {
  if (!pet) return null;
  return {
    ...pet,
    spritesheetUrl: absolutizeManifestAsset(pet.spritesheetUrl, manifestURL),
    petJsonUrl: absolutizeManifestAsset(pet.petJsonUrl, manifestURL),
    zipUrl: absolutizeManifestAsset(pet.zipUrl, manifestURL),
  };
}

function normalizePet(input) {
  if (!input || typeof input !== "object") return null;
  const slug = cleanString(input.slug || input.id);
  const displayName = cleanString(input.displayName || input.name || slug);
  const spritesheetUrl = cleanString(
    input.spritesheetUrl ||
      input.spritesheetURL ||
      input.spritesheetPath ||
      input.spritesheet ||
      input.spriteUrl ||
      input.spriteURL,
  );

  if (!slug || !displayName || !spritesheetUrl) return null;

  return {
    source: cleanString(input.source) || PREBUNDLED_SOURCE,
    nativePetId: cleanString(input.nativePetId),
    slug,
    displayName,
    description: cleanString(input.description) || "Animated Codex-compatible pet.",
    kind: cleanString(input.kind) || "pet",
    submittedBy: cleanString(input.submittedBy || input.author),
    tags: listStrings(input.tags || input.vibes),
    spritesheetUrl,
    petJsonUrl: cleanString(input.petJsonUrl || input.petJSONUrl || input.petJsonURL),
    zipUrl: cleanString(input.zipUrl || input.zipURL),
    frameWidth: positiveNumber(input.frameWidth) || 192,
    frameHeight: positiveNumber(input.frameHeight) || 208,
    canUninstall: Boolean(input.canUninstall),
  };
}

function chooseInitialPet() {
  if (state.selected && allPets().some((pet) => samePet(pet, state.selected))) return;
  state.selected =
    state.installed[0] ||
    state.catalog.find((pet) => pet.slug === "boba") ||
    state.catalog[0] ||
    null;
}

function render() {
  renderList();
  renderSelected();
}

function setSelectedPet(pet) {
  state.selected = pet;
  state.lastMessage = "";
  updateListSelection();
  renderStatus();
  renderSelected();
}

function renderList() {
  const pets = filteredPets();
  const fragment = document.createDocumentFragment();

  for (const pet of pets) {
    const row = els.rowTemplate.content.firstElementChild.cloneNode(true);
    row.dataset.slug = pet.slug;
    row.dataset.source = pet.source;
    row.dataset.key = rowKey(pet);
    row.setAttribute("aria-selected", samePet(pet, state.selected) ? "true" : "false");
    row.classList.toggle("is-selected", samePet(pet, state.selected));
    row.classList.toggle("is-active", pet.slug === state.activePetSlug);
    row.querySelector(".pet-row-name").textContent = pet.displayName;
    row.querySelector(".pet-row-meta").textContent = rowMeta(pet);
    row.addEventListener("click", () => {
      setSelectedPet(pet);
    });
    fragment.append(row);
  }

  els.list.replaceChildren(fragment);
  renderStatus();
}

// Selection changes only toggle row classes instead of rebuilding the list,
// which keeps scroll position and avoids DOM churn.
function updateListSelection() {
  const selectedKey = state.selected ? rowKey(state.selected) : "";
  for (const row of els.list.querySelectorAll(".pet-row")) {
    const selected = row.dataset.key === selectedKey;
    row.classList.toggle("is-selected", selected);
    row.classList.toggle("is-active", row.dataset.slug === state.activePetSlug);
    row.setAttribute("aria-selected", selected ? "true" : "false");
  }
}

function rowKey(pet) {
  return `${pet.source}|${pet.slug}|${pet.nativePetId || ""}`;
}

function renderStatus() {
  if (state.lastMessage) {
    setStatus(state.lastMessage);
    return;
  }
  const pets = filteredPets();
  if (pets.length === 0) {
    setStatus("No matching pets");
    return;
  }
  const installedCount = state.installed.length;
  const total = allPets().length;
  const suffix = installedCount > 0 ? `, ${installedCount} installed` : "";
  setStatus(`${formatCount(pets.length)} of ${formatCount(total)} pets${suffix}`);
}

function renderSelected() {
  const pet = state.selected;
  if (!pet) {
    els.sourceLabel.textContent = "Petdex";
    els.petName.textContent = "No Pet Selected";
    els.petDescription.textContent = "Choose a pet to preview it.";
    els.permissionProfile.textContent = "";
    if (previewKey !== "empty") {
      previewKey = "empty";
      els.spritePreview.replaceChildren(emptyPreview());
    }
    updateAction();
    return;
  }

  els.sourceLabel.textContent = sourceLabel(pet);
  els.petName.textContent = pet.displayName;
  els.petDescription.textContent = pet.description;
  els.permissionProfile.textContent = permissionProfileText(pet);
  // Rebuild the sprite only when it actually changed: unrelated re-renders
  // (status events, busy flags) must not re-decode the sheet or restart the
  // idle animation.
  const key = previewIdentity(pet);
  if (previewKey !== key) {
    previewKey = key;
    els.spritePreview.replaceChildren(createSpriteElement(pet));
  }
  updateAction();
}

function previewIdentity(pet) {
  return [pet.source, pet.slug, pet.nativePetId || "", pet.spritesheetUrl, pet.frameWidth, pet.frameHeight].join("|");
}

function updateAction() {
  const pet = state.selected;
  const isActive = pet && state.activePetSlug && pet.slug === state.activePetSlug;
  const canUse =
    state.nativeReady &&
    !state.busy &&
    !isActive &&
    (isCatalogPet(pet) || (pet?.source === "installed" && pet.nativePetId));
  els.primaryAction.disabled = !canUse;
  els.primaryAction.textContent = isActive ? "Active" : "Use";

  updatePiExtensionStatus();

  const showPiAction =
    state.nativeReady &&
    state.piExtension.available &&
    (!state.piExtension.installed || state.piExtension.needsUpdate || state.piInstallBusy);
  els.installPiAction.hidden = !showPiAction;
  els.installPiAction.disabled = !showPiAction || state.busy || state.piInstallBusy || state.piUninstallBusy;
  els.installPiAction.textContent = state.piInstallBusy
    ? "Installing..."
    : state.piExtension.installed && state.piExtension.needsUpdate
      ? "Update Pi"
      : "Install to Pi";

  const showUninstallPiAction =
    state.nativeReady &&
    state.piExtension.installed;
  els.uninstallPiAction.hidden = !showUninstallPiAction;
  els.uninstallPiAction.disabled =
    !showUninstallPiAction || state.busy || state.piInstallBusy || state.piUninstallBusy;
  els.uninstallPiAction.textContent = state.piUninstallBusy ? "Uninstalling..." : "Uninstall Pi";
}

function updatePiExtensionStatus() {
  const status = (() => {
    if (!state.nativeReady) return "Pi extension status unavailable";
    if (state.piInstallBusy) return "Pi extension installing...";
    if (state.piUninstallBusy) return "Pi extension uninstalling...";
    if (state.piExtension.installed && !state.piExtension.available) {
      return "Pi extension installed - source unavailable";
    }
    if (!state.piExtension.available) return "Pi extension source unavailable";
    if (state.piExtension.installed && state.piExtension.needsUpdate) {
      const version = state.piExtension.sourceVersion ? ` to ${state.piExtension.sourceVersion}` : "";
      return `Pi extension installed - update available${version}`;
    }
    if (state.piExtension.installed) {
      return state.piExtension.installedVersion
        ? `Pi extension installed (${state.piExtension.installedVersion})`
        : "Pi extension installed";
    }
    return "Pi extension not installed";
  })();

  els.piExtensionStatus.textContent = status;
  els.piExtensionStatus.title = [
    state.piExtension.path,
    state.piExtension.installedVersion ? `Installed: ${state.piExtension.installedVersion}` : "",
    state.piExtension.sourceVersion ? `Bundled: ${state.piExtension.sourceVersion}` : "",
  ].filter(Boolean).join("\n") || status;
}

function useSelectedPet() {
  if (!state.selected || state.busy) return;
  if (state.selected.slug === state.activePetSlug) return;

  if (state.selected.source === "installed") {
    if (!state.selected.nativePetId) return;
    state.busy = true;
    state.lastMessage = `Selecting ${state.selected.displayName}...`;
    postNativeMessage({
      action: "selectInstalledPet",
      petId: state.selected.nativePetId,
    });
    render();
    return;
  }

  if (!isCatalogPet(state.selected)) return;

  // If this catalog pet is already installed, select it instead of importing a duplicate.
  const alreadyInstalled = state.installed.find((p) => p.slug === state.selected.slug);
  if (alreadyInstalled && alreadyInstalled.nativePetId) {
    state.busy = true;
    state.lastMessage = `Selecting ${state.selected.displayName}...`;
    postNativeMessage({
      action: "selectInstalledPet",
      petId: alreadyInstalled.nativePetId,
    });
    render();
    return;
  }

  if (!state.selected.nativePetId) return;
  state.busy = true;
  state.lastMessage = `Selecting ${state.selected.displayName}...`;
  postNativeMessage({
    action: "importPet",
    petId: state.selected.nativePetId,
  });
  render();
}

function installPiExtension() {
  const canInstall =
    state.nativeReady &&
    state.piExtension.available &&
    (!state.piExtension.installed || state.piExtension.needsUpdate);
  if (!canInstall || state.busy || state.piInstallBusy || state.piUninstallBusy) return;
  state.piInstallBusy = true;
  state.lastMessage = "Installing Pi extension...";
  postNativeMessage({ action: "installPiExtension" });
  render();
}

function uninstallPiExtension() {
  const canUninstall =
    state.nativeReady &&
    state.piExtension.installed;
  if (!canUninstall || state.busy || state.piInstallBusy || state.piUninstallBusy) return;
  state.piUninstallBusy = true;
  state.lastMessage = "Uninstalling Pi extension...";
  postNativeMessage({ action: "uninstallPiExtension" });
  render();
}

function handleNativeReady() {
  state.nativeReady = true;
  document.documentElement.classList.add("native-shell");
  requestBrowserPets();
  requestPiExtensionStatus();
  render();
}

function requestBrowserPets() {
  if (!nativeBridgeAvailable()) return;
  postNativeMessage({ action: "listBrowserPets" });
}

function requestPiExtensionStatus() {
  if (!nativeBridgeAvailable()) return;
  postNativeMessage({ action: "getPiExtensionStatus" });
}

function updatePiExtensionState(detail) {
  if (!detail || typeof detail !== "object") return;
  state.piExtension = {
    available: Boolean(detail.available),
    installed: Boolean(detail.installed),
    needsUpdate: Boolean(detail.needsUpdate),
    path: cleanString(detail.path),
    sourceVersion: cleanString(detail.sourceVersion),
    installedVersion: cleanString(detail.installedVersion),
  };
}

function postNativeMessage(payload) {
  if (window.CodexPetsNative?.postMessage) {
    window.CodexPetsNative.postMessage(payload);
    return true;
  }
  if (window.webkit?.messageHandlers?.codexPets?.postMessage) {
    window.webkit.messageHandlers.codexPets.postMessage(payload);
    return true;
  }
  return false;
}

function nativeBridgeAvailable() {
  return Boolean(
    window.CodexPetsNative?.postMessage ||
      window.webkit?.messageHandlers?.codexPets?.postMessage,
  );
}

function createSpriteElement(pet) {
  const frameWidth = pet.frameWidth || 192;
  const frameHeight = pet.frameHeight || 208;
  const scale = spriteScale(frameWidth, frameHeight);

  const frame = document.createElement("span");
  frame.className = "pet-sprite-frame";
  frame.setAttribute("role", "img");
  frame.setAttribute("aria-label", pet.displayName);
  frame.style.setProperty("--frame-w", `${frameWidth}px`);
  frame.style.setProperty("--frame-h", `${frameHeight}px`);
  frame.style.setProperty("--sheet-w", `${frameWidth * 8}px`);
  frame.style.setProperty("--sheet-h", `${frameHeight * 9}px`);
  frame.style.setProperty("--scale", String(scale));

  const sprite = document.createElement("span");
  sprite.className = "pet-sprite";
  sprite.style.setProperty("--sprite-url", `url("${cssEscapeUrl(pet.spritesheetUrl)}")`);
  sprite.style.setProperty("--sprite-frames", String(IDLE_STATE.frames));
  sprite.style.setProperty("--sprite-duration", `${IDLE_STATE.durationMs}ms`);
  sprite.style.setProperty("--sprite-end-x", `-${IDLE_STATE.frames * frameWidth}px`);
  frame.append(sprite);
  return frame;
}

function emptyPreview() {
  const element = document.createElement("span");
  element.className = "empty-preview";
  element.textContent = "No preview";
  return element;
}

function spriteScale(frameWidth, frameHeight) {
  const bounds = els.spritePreview.getBoundingClientRect();
  const availableWidth = Math.max(80, bounds.width - 12);
  const availableHeight = Math.max(80, bounds.height - 12);
  const fit = Math.min(availableWidth / frameWidth, availableHeight / frameHeight, 1.85);
  return Math.max(0.35, fit);
}

function selectAdjacentPet(direction) {
  const pets = filteredPets();
  if (pets.length === 0) return;
  const index = Math.max(0, pets.findIndex((pet) => samePet(pet, state.selected)));
  const nextIndex = Math.min(pets.length - 1, Math.max(0, index + direction));
  setSelectedPet(pets[nextIndex]);
  els.list
    .querySelector(`[data-source="${state.selected.source}"][data-slug="${cssSelectorEscape(state.selected.slug)}"]`)
    ?.scrollIntoView({ block: "nearest" });
}

function filteredPets() {
  const query = state.query;
  if (!query) return allPets();

  return allPets().filter((pet) => {
    const haystack = [
      pet.displayName,
      pet.slug,
      pet.kind,
      pet.submittedBy,
      pet.description,
      permissionProfile(pet).label,
      permissionProfile(pet).detail,
      ...pet.tags,
      pet.source,
    ]
      .join(" ")
      .toLowerCase();
    return haystack.includes(query);
  });
}

function allPets() {
  // Installed rows arrive already deduplicated by the daemon; the page only
  // hides catalog rows that duplicate an installed pet.
  const installed = state.installed;
  const installedSlugs = new Set(installed.map((p) => p.slug));
  const installedNames = new Set(installed.map((p) => normalizedPetName(p)));
  const uniqueCatalog = state.catalog.filter(
    (p) => !installedSlugs.has(p.slug) && !installedNames.has(normalizedPetName(p)),
  );
  return [...installed, ...uniqueCatalog];
}

function normalizedPetName(pet) {
  return cleanString(pet?.displayName || pet?.slug).toLowerCase();
}

function isCatalogPet(pet) {
  return pet?.source === PREBUNDLED_SOURCE;
}

function samePet(left, right) {
  if (!left || !right) return false;
  if (left.source === "installed" || right.source === "installed") {
    return left.nativePetId && right.nativePetId
      ? left.nativePetId === right.nativePetId
      : left.source === right.source && left.slug === right.slug;
  }
  return left.source === right.source && left.slug === right.slug;
}

function rowMeta(pet) {
  const detail = displayAttribution(pet) || (pet.source === "installed" ? "" : pet.kind);
  const profile = permissionProfile(pet).shortLabel;
  return [profile, detail].filter(Boolean).join(" - ");
}

function sourceLabel(pet) {
  const profile = permissionProfile(pet).label;
  const attribution = displayAttribution(pet);
  const source =
    pet.source === "installed"
      ? attribution ? `Installed - ${attribution}` : "Installed"
      : attribution ? `Bundled - ${attribution}` : "Bundled";
  return [source, profile].filter(Boolean).join(" - ");
}

function displayAttribution(pet) {
  const value = cleanString(pet?.submittedBy);
  if (pet?.source === "installed" && value.toLowerCase() === "imported") return "";
  return value;
}

function permissionProfileText(pet) {
  const profile = permissionProfile(pet);
  return `Pi permissions: ${profile.detail}`;
}

function permissionProfile(pet) {
  return PERMISSION_PROFILES[pet?.slug] || PERMISSION_PROFILES.default;
}

function absolutizeAsset(value, base) {
  if (!value) return "";
  if (/^(https?|file|data|blob):/i.test(value)) return value;
  return `${trimSlash(base)}/${String(value).replace(/^\/+/, "")}`;
}

function absolutizeManifestAsset(value, manifestURL) {
  if (!value) return "";
  if (/^(https?|file|data|blob):/i.test(value)) return value;
  try {
    return new URL(value, manifestURL).href;
  } catch {
    return value;
  }
}

function trimSlash(value) {
  return String(value).replace(/\/+$/, "");
}

function cleanString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function listStrings(value) {
  if (!Array.isArray(value)) return [];
  return value.map(cleanString).filter(Boolean).slice(0, 12);
}

function positiveNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : null;
}

function formatCount(value) {
  return new Intl.NumberFormat("en-US").format(value);
}

function cssEscapeUrl(value) {
  return String(value).replace(/"/g, '\\"');
}

function cssSelectorEscape(value) {
  if (window.CSS?.escape) return CSS.escape(value);
  return String(value).replace(/["\\]/g, "\\$&");
}

function setStatus(message) {
  els.status.textContent = message;
}

// Resize only rescales the existing sprite frame (rAF-debounced); rebuilding
// the element would re-decode the sheet and restart the animation.
window.addEventListener("resize", () => {
  if (!state.selected) return;
  cancelAnimationFrame(resizeFrame);
  resizeFrame = requestAnimationFrame(() => {
    const frame = els.spritePreview.querySelector(".pet-sprite-frame");
    if (!frame || !state.selected) return;
    const scale = spriteScale(state.selected.frameWidth || 192, state.selected.frameHeight || 208);
    frame.style.setProperty("--scale", String(scale));
  });
});

// The window outlives close (it is only hidden), so pause the idle animation
// whenever the page is not visible to keep the web process quiet.
document.addEventListener("visibilitychange", () => {
  document.documentElement.classList.toggle("page-hidden", document.visibilityState !== "visible");
});
