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

// A Magpie/OpenCode plugin names its provider surface in one of two shapes:
//
//   provider: { workbuddy: { models() {}, ... } }   keyed by provider id
//   provider: { id: "workbuddy", models() {} }      one provider, hooks inline
//
// The second shape is what every package built on OpenCode 2.x's
// {id, setup(ctx)} convention returns, so a probe that only walked object values
// read its `models` function as a nested provider table and found no hooks at
// all: the account showed zero models and no allowance while the plugin was
// perfectly healthy.
function providerSurfaces(provider) {
  if (!provider || typeof provider === "function") return [];
  const looksLikeOneProvider =
    typeof provider.id === "string" ||
    typeof provider.models === "function" ||
    typeof provider.listModels === "function" ||
    typeof provider.catalog === "function" ||
    typeof provider.usage === "function";
  if (looksLikeOneProvider) {
    const id = typeof provider.id === "string" ? provider.id : "";
    return [{ id, hooks: provider }];
  }
  return Object.entries(provider)
    .filter(([, value]) => value && typeof value === "object")
    .map(([id, value]) => ({ id, hooks: value }));
}

function normaliseModel(item) {
  if (typeof item === "string") return item;
  if (item && typeof item === "object") {
    const id = item.id ?? item.name ?? item.model;
    if (typeof id === "string") return id;
  }
  return "";
}

// modelsOf reads whatever a provider's model hook produced: an array, a
// {models: [...]} envelope, or the id-keyed record OpenCode 2.x returns.
function modelsOf(produced) {
  if (Array.isArray(produced)) return produced;
  if (produced && typeof produced === "object") {
    if (Array.isArray(produced.models)) return produced.models;
    return Object.values(produced);
  }
  return [];
}

async function collectModels(provider) {
  const models = [];
  for (const surface of providerSurfaces(provider)) {
    const list = surface.hooks.models ?? surface.hooks.listModels ?? surface.hooks.catalog;
    if (typeof list !== "function") continue;
    let produced;
    try {
      // A models hook may inspect the account it is given. A probe has no
      // credential to hand it, so it is called the way an unauthenticated list
      // is called; a hook that needs one answers the plugin's own default list.
      produced = await list(
        { id: surface.id, models: surface.hooks.models },
        { auth: undefined, provider: { id: surface.id } },
      );
    } catch {
      continue;
    }
    for (const row of modelsOf(produced)) {
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

// configSurface runs a plugin's config hook against a scratch config and returns
// the provider ids and models it declares.
//
// config is a producer: the plugin is handed the object to fill in and writes
// provider.<id>.models there. Many packages publish their whole model catalog
// this way and leave provider.models to answer the signed-in account's own list,
// so a probe that only called provider.models reported zero models for an
// installed, healthy plugin.
async function configSurface(hooks) {
  if (typeof hooks?.config !== "function") return { ids: [], models: [] };
  let scratch;
  try {
    scratch = {};
    const produced = await hooks.config(scratch);
    // A config hook may fill the object it is given or return one instead.
    if (produced && typeof produced === "object" && produced.provider) scratch = produced;
  } catch {
    return { ids: [], models: [] };
  }
  const provider = scratch?.provider;
  if (!provider || typeof provider !== "object") return { ids: [], models: [] };
  const models = [];
  for (const value of Object.values(provider)) {
    const table = value?.models;
    if (!table || typeof table !== "object") continue;
    for (const id of Object.keys(table)) {
      if (id && !models.includes(id)) models.push(id);
      if (models.length >= 500) return { ids: Object.keys(provider), models };
    }
  }
  return { ids: Object.keys(provider), models };
}

async function collectWindows(provider) {
  for (const surface of providerSurfaces(provider)) {
    // usage lives on the provider hooks or beside them under auth, which is where
    // an OpenCode 2.x-style plugin puts it.
    const read = surface.hooks.usage ?? surface.hooks.auth?.usage ?? surface.hooks.allowance;
    if (typeof read !== "function") continue;
    try {
      const windows = normaliseWindows(
        await read({ provider: surface.id }, { auth: undefined, provider: { id: surface.id } }),
      );
      if (windows.length > 0) return windows;
    } catch {
      // A usage hook that throws must not hide the model list.
      continue;
    }
  }
  return [];
}

// collectAuthWindows reads usage from the auth hooks, the other place a plugin
// may publish it.
async function collectAuthWindows(auth) {
  const read = auth?.usage;
  if (typeof read !== "function") return [];
  try {
    return normaliseWindows(await read({}, { auth: undefined, provider: {} }));
  } catch {
    return [];
  }
}

const timeout = setTimeout(() => fail("plugin probe timed out inside the sidecar"), 15000);
timeout.unref?.();

try {
  const hooks = await loadHooks();
  const warnings = [];
  const config = await configSurface(hooks);
  let models = [];
  let windows = [];
  try {
    models = await collectModels(hooks.provider);
  } catch (error) {
    warnings.push(`models hook failed: ${String(error?.message ?? error).slice(0, 120)}`);
  }
  if (models.length === 0) models = config.models;
  try {
    windows = await collectWindows(hooks.provider);
    if (windows.length === 0) windows = await collectAuthWindows(hooks.auth);
  } catch (error) {
    warnings.push(`usage hook failed: ${String(error?.message ?? error).slice(0, 120)}`);
  }
  const providerId =
    providerSurfaces(hooks.provider)[0]?.id || config.ids[0] || manifest.magpie?.id || manifest.name || "";
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
