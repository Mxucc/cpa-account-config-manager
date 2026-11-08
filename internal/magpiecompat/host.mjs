// Magpie/OpenCode provider plugin sidecar.
//
// Invoked as: bun host.mjs <plugin-directory>
// It imports the plugin package, reads three hooks and writes ONE JSON line:
//   {"ok":true,"result":{"id","methods":[],"models":[],"windows":[]}}
//   {"ok":false,"error":"..."}
//
// Deliberately narrow: `fetch` is never called, so the plugin cannot proxy or
// rewrite requests. Nothing is written to disk, no credentials are read, and no
// environment secrets are forwarded. The host enforces the timeout and the
// output cap, so this file only has to be honest about what it found.
import { pathToFileURL } from "node:url";
import { createRequire } from "node:module";
import path from "node:path";

const directory = process.argv[2];
const say = (value) => process.stdout.write(`${JSON.stringify(value)}\n`);

function fail(message) {
  say({ ok: false, error: String(message).slice(0, 500) });
  process.exit(0);
}

if (!directory) fail("plugin directory is required");

let manifest = {};
try {
  manifest = createRequire(path.join(directory, "package.json"))("./package.json");
} catch {
  // A missing manifest is not fatal: the entry file is enough for a probe.
}

function entryFile() {
  const main = typeof manifest.main === "string" ? manifest.main : "";
  if (main) return path.resolve(directory, main);
  const exp = manifest.exports;
  if (typeof exp === "string") return path.resolve(directory, exp);
  if (exp && typeof exp === "object" && typeof exp["."] === "string") return path.resolve(directory, exp["."]);
  return path.resolve(directory, "index.mjs");
}

// The input mirrors the small surface Magpie gives a plugin, minus anything that
// could act: auth.set is recorded in memory only, and there is no fetch.
const input = {
  directory,
  client: {
    auth: {
      set: async () => undefined,
      get: async () => undefined,
    },
    app: { log: () => undefined },
    config: { get: async () => ({}) },
  },
  magpie: {
    usage: () => undefined,
    option: () => false,
    options: () => ({}),
    account: () => undefined,
  },
  $: () => {
    throw new Error("shell access is not available to a probed plugin");
  },
};

function pickHooks(value) {
  if (!value || typeof value !== "object") return null;
  if (value.provider || value.auth) return value;
  if (value.default && typeof value.default === "object" && (value.default.provider || value.default.auth)) {
    return value.default;
  }
  return null;
}

async function loadHooks() {
  const module = await import(pathToFileURL(entryFile()).href);
  const direct = pickHooks(module);
  if (direct) return direct;
  for (const exported of Object.values(module)) {
    if (typeof exported !== "function") continue;
    const produced = pickHooks(await exported(input));
    if (produced) return produced;
  }
  throw new Error("the plugin exported no provider or auth hooks");
}

function methodNames(auth) {
  if (!auth) return [];
  const methods = auth.methods ?? auth.authMethods ?? auth.logins;
  if (Array.isArray(methods)) {
    return methods
      .map((item) => (typeof item === "string" ? item : item?.label ?? item?.name ?? item?.id))
      .filter((item) => typeof item === "string" && item.trim() !== "")
      .slice(0, 8);
  }
  return [];
}

function providerEntries(provider) {
  if (!provider) return [];
  if (typeof provider === "function") return [];
  return Object.entries(provider).filter(([, value]) => value && typeof value === "object");
}

function normaliseModel(item) {
  if (typeof item === "string") return item;
  if (item && typeof item === "object") {
    const id = item.id ?? item.name ?? item.model;
    if (typeof id === "string") return id;
  }
  return "";
}

async function collectModels(provider) {
  const models = [];
  for (const [, hooks] of providerEntries(provider)) {
    const list = hooks.models ?? hooks.listModels ?? hooks.catalog;
    if (typeof list !== "function") continue;
    const produced = await list();
    const rows = Array.isArray(produced) ? produced : produced?.models;
    if (!Array.isArray(rows)) continue;
    for (const row of rows) {
      const id = normaliseModel(row);
      if (id && !models.includes(id)) models.push(id);
      if (models.length >= 500) return models;
    }
  }
  return models;
}

function number(value) {
  const parsed = typeof value === "number" ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function normaliseWindows(usage) {
  if (!usage || typeof usage !== "object") return [];
  const raw = Array.isArray(usage) ? usage : usage.windows ?? usage;

  if (Array.isArray(raw)) {
    return raw
      .filter((item) => item && typeof item === "object" && typeof item.name === "string" && item.name.trim())
      .slice(0, 12)
      .map((item) => ({
        name: item.name.trim().slice(0, 60),
        used: number(item.used ?? item.usedPercent ?? item.used_percent),
        limit: number(item.limit ?? item.amount),
        balance: number(item.balance),
        unit: typeof item.unit === "string" ? item.unit.slice(0, 12) : undefined,
        reset: typeof item.reset === "string" ? item.reset.slice(0, 40) : undefined,
        aside: item.aside === true,
      }));
  }

  // Magpie names its windows after the period; keep them if a plugin answers so.
  const named = [
    ["five_hour", usage.fiveHour ?? usage.five_hour],
    ["weekly", usage.weekly],
    ["monthly", usage.monthly],
  ];
  return named
    .filter(([, value]) => value && typeof value === "object")
    .map(([name, value]) => ({
      name,
      used: number(value.used ?? value.usedPercent),
      limit: number(value.limit),
      balance: number(value.balance),
      unit: typeof value.unit === "string" ? value.unit.slice(0, 12) : undefined,
      reset: typeof value.reset === "string" ? value.reset.slice(0, 40) : undefined,
      aside: value.aside === true,
    }));
}

async function collectWindows(provider) {
  for (const [id, hooks] of providerEntries(provider)) {
    const read = hooks.usage ?? hooks.auth?.usage ?? hooks.allowance;
    if (typeof read !== "function") continue;
    try {
      const windows = normaliseWindows(await read({ provider: id }));
      if (windows.length > 0) return windows;
    } catch (error) {
      // A usage hook that throws must not hide the model list.
      return [];
    }
  }
  return [];
}

const timeout = setTimeout(() => fail("plugin probe timed out inside the sidecar"), 15000);
timeout.unref?.();

try {
  const hooks = await loadHooks();
  const warnings = [];
  let models = [];
  let windows = [];
  try {
    models = await collectModels(hooks.provider);
  } catch (error) {
    warnings.push(`models hook failed: ${String(error?.message ?? error).slice(0, 120)}`);
  }
  try {
    windows = await collectWindows(hooks.provider);
  } catch (error) {
    warnings.push(`usage hook failed: ${String(error?.message ?? error).slice(0, 120)}`);
  }
  const providerId = providerEntries(hooks.provider)[0]?.[0] ?? manifest.magpie?.id ?? manifest.name ?? "";
  say({
    ok: true,
    result: {
      id: String(providerId).slice(0, 80),
      methods: methodNames(hooks.auth),
      models: models.slice(0, 500),
      windows,
      warnings,
    },
  });
} catch (error) {
  fail(error?.message ?? error);
}
