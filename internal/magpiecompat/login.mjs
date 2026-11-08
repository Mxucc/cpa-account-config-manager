// Magpie/OpenCode provider plugin sign-in sidecar.
//
// Invoked as: bun login.mjs <plugin-directory> <action> [json-argument]
//
//	login.mjs <dir> methods
//	    -> {"ok":true,"result":{"provider":"zcode","name":"...","methods":[{"id","type","label","form"}]}}
//	login.mjs <dir> run  {"status_file":"...","method":"v1-0","inputs":{}}
//	    Runs the whole sign-in in this one process and reports through the status
//	    file: first {"phase":"waiting","url","instructions","mode"}, then
//	    {"phase":"success","credential"} or {"phase":"failed","message"}.
//
// The flow is driven in a single process on purpose: a plugin's `authorize()`
// returns a closure that polls its own flow id, so a second process could not
// finish a sign-in the first one started. The host therefore starts this file
// once, reads the URL from the status file, and polls the same file.
//
// This is the only file in the plugin that runs a community plugin's own sign-in.
// It is deliberately narrow:
//   - one process per action, so a hung sign-in cannot leak into the next call;
//   - `fetch` is NOT provided to the plugin, so the plugin cannot proxy requests
//     or read anything the sidecar was not asked for;
//   - `client.auth.set` only records the credential in memory and prints it once,
//     at the end, to the caller that asked for the sign-in;
//   - the host enforces the timeout and the output cap.
//
// A plugin's auth hooks are the shape Magpie documents: either
// { auth: { methods: [...] } } or a factory that receives the plugin input and
// returns the same. A method with `authorize()` runs an interactive flow and
// reports the credential through its returned callback; a method with only a
// label is a pasted key.
import { pathToFileURL } from "node:url";
import { createRequire } from "node:module";
import { writeFileSync } from "node:fs";
import path from "node:path";

const directory = process.argv[2];
const action = (process.argv[3] || "methods").toLowerCase();
const argument = (() => {
  const raw = process.argv[4];
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
})();

const say = (value) => process.stdout.write(`${JSON.stringify(value)}\n`);
const fail = (message) => {
  say({ ok: false, error: String(message).slice(0, 600) });
  process.exit(0);
};

if (!directory) fail("plugin directory is required");

let manifest = {};
try {
  manifest = createRequire(path.join(directory, "package.json"))("./package.json");
} catch {
  // A missing manifest is not fatal: the entry file is enough.
}

function entryFile() {
  const main = typeof manifest.main === "string" ? manifest.main : "";
  if (main) return path.resolve(directory, main);
  const exp = manifest.exports;
  if (typeof exp === "string") return path.resolve(directory, exp);
  if (exp && typeof exp === "object" && typeof exp["./"] === "string") return path.resolve(directory, exp["./"]);
  if (exp && typeof exp === "object" && typeof exp["."] === "string") return path.resolve(directory, exp["."]);
  return path.resolve(directory, "index.mjs");
}

// The credential a plugin saves through client.auth.set, captured in memory only.
const captured = { value: null };

// The input mirrors what Magpie gives a plugin, minus anything that could act on
// the machine: no fetch, no shell, no filesystem beyond the plugin's own module.
function pluginInput(log) {
  return {
    directory,
    client: {
      auth: {
        // A sign-in saves its credential here; the host reads it from the one
        // line this process prints, so nothing is written to disk.
        set: async (arg) => {
          const body = arg && typeof arg === "object" ? arg.body ?? arg : null;
          if (body && typeof body === "object") captured.value = body;
          return { data: true };
        },
        get: async () => ({ data: undefined }),
      },
      app: {
        log: (entry) => {
          const message = entry?.body?.message ?? entry?.message ?? "";
          if (message) log(String(message).slice(0, 300));
        },
      },
      config: { get: async () => ({}) },
    },
    magpie: {
      usage: () => undefined,
      option: () => false,
      options: () => ({}),
      account: () => undefined,
    },
    $: () => {
      throw new Error("shell access is not available to a sign-in");
    },
  };
}

function pickHooks(value) {
  if (!value || typeof value !== "object") return null;
  if (value.provider || value.auth) return value;
  if (value.default && typeof value.default === "object" && (value.default.provider || value.default.auth)) {
    return value.default;
  }
  return null;
}

async function loadHooks(log) {
  const module = await import(pathToFileURL(entryFile()).href);
  const direct = pickHooks(module);
  if (direct) return direct;
  for (const exported of Object.values(module)) {
    if (typeof exported !== "function") continue;
    let produced;
    try {
      produced = await exported(pluginInput(log));
    } catch {
      continue;
    }
    const hooks = pickHooks(produced);
    if (hooks) return hooks;
  }
  throw new Error("the plugin exported no provider or auth hooks");
}

// methodList normalizes the plugin's auth methods into one shape the page can draw.
function methodList(auth) {
  const raw = auth?.methods ?? auth?.authMethods ?? auth?.logins;
  if (!Array.isArray(raw)) return [];
  return raw
    .map((entry, index) => {
      if (!entry || typeof entry !== "object") return null;
      const label = String(entry.label ?? entry.name ?? entry.id ?? `method ${index + 1}`).slice(0, 120);
      const type = typeof entry.type === "string" ? entry.type : typeof entry.authorize === "function" ? "oauth" : "key";
      const form = normalizeForm(entry.prompts ?? entry.form ?? entry.fields);
      return {
        id: `v1-${index}`,
        index,
        type,
        label,
        interactive: typeof entry.authorize === "function",
        ...(form.length ? { form } : {}),
      };
    })
    .filter(Boolean);
}

// normalizeForm reads a method's declared inputs, so a sign-in that needs a site
// or a token is asked for instead of guessed.
function normalizeForm(raw) {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((field, index) => {
      if (typeof field === "string") return { key: field, label: field, kind: "text", required: false };
      if (!field || typeof field !== "object") return null;
      const key = String(field.key ?? field.name ?? field.id ?? `field${index}`).slice(0, 60);
      const label = String(field.label ?? field.title ?? key).slice(0, 120);
      const options = Array.isArray(field.options)
        ? field.options.map((option) => String(option?.value ?? option?.label ?? option).slice(0, 60)).filter(Boolean)
        : Array.isArray(field.values)
          ? field.values.map((option) => String(option).slice(0, 60)).filter(Boolean)
          : [];
      return {
        key,
        label,
        kind: options.length ? "select" : typeof field.type === "string" ? field.type.slice(0, 20) : "text",
        required: field.required === true,
        ...(options.length ? { options } : {}),
      };
    })
    .filter(Boolean);
}

// providerBaseURL reads the API base the provider declares, which is what a CPA
// channel needs to route the provider's models.
function providerBaseURL(hooks, method) {
  const candidates = [
    method?.provider?.api,
    method?.api,
    hooks?.provider?.api,
    hooks?.config?.api,
    manifest?.magpie?.baseURL,
    manifest?.magpie?.base_url,
  ];
  for (const candidate of candidates) {
    if (typeof candidate === "string" && /^https?:\/\//i.test(candidate.trim())) {
      return candidate.trim().slice(0, 300);
    }
  }
  return "";
}

function providerID(hooks) {
  const provider = hooks?.provider;
  if (provider && typeof provider === "object") {
    if (typeof provider.id === "string" && provider.id.trim()) return provider.id.trim();
    const first = Object.keys(provider)[0];
    if (first) return first;
  }
  if (typeof manifest.magpie?.id === "string" && manifest.magpie.id.trim()) return manifest.magpie.id.trim();
  return String(manifest.name ?? "").slice(0, 80);
}

async function run() {
  const log = (message) => {
    // Diagnostics go to stderr so stdout stays one JSON line.
    try {
      process.stderr.write(`${message}\n`);
    } catch {}
  };
  const hooks = await loadHooks(log);
  const auth = hooks.auth;

  if (action === "methods") {
    const methods = methodList(auth);
    say({
      ok: true,
      result: {
        provider: providerID(hooks),
        name: String(manifest.magpie?.name?.zh ?? manifest.magpie?.name?.en ?? manifest.name ?? "").slice(0, 120),
        methods,
      },
    });
    return;
  }

  if (action !== "run") fail(`unknown action: ${action}`);

  const statusFile = String(argument.status_file ?? "").trim();
  if (!statusFile) fail("a status file is required to report the sign-in");
  const report = (value) => {
    try {
      writeFileSync(statusFile, `${JSON.stringify(value)}\n`, { mode: 0o600 });
    } catch (error) {
      fail(`the sign-in could not report its state: ${error?.message ?? error}`);
    }
  };

  const methods = methodList(auth);
  const wanted = String(argument.method ?? "").trim();
  const method = methods.find((entry) => entry.id === wanted) ?? methods.find((entry) => entry.interactive) ?? methods[0];
  if (!method) fail("this plugin declares no sign-in method");
  const raw = Array.isArray(auth?.methods) ? auth.methods[method.index] : null;
  if (!raw) fail("this plugin declares no sign-in method");
  // An api method answers a credential directly: keyless ones need nothing from the
  // operator, and a keyed one asks for the key. Either way there is no browser step,
  // so authorize() is called with the inputs and its answer is the credential.
  if (String(raw.type ?? "").toLowerCase() === "api") {
    const inputs = argument.inputs && typeof argument.inputs === "object" ? argument.inputs : {};
    if (raw.keyless !== true) {
      const pasted = String(inputs.key ?? inputs.api_key ?? inputs.token ?? "").trim();
      if (!pasted) {
        report({
          phase: "waiting",
          provider: providerID(hooks),
          method: method.id,
          mode: "key",
          url: "",
          instructions: String(raw.label ?? "Paste the provider's API key."),
        });
        return;
      }
      inputs.key = pasted;
    }
    const answer = await raw.authorize(
      Object.fromEntries(Object.entries(inputs).map(([key, value]) => [key, String(value)])),
    );
    if (!answer || typeof answer !== "object") fail("the sign-in method answered nothing");
    if (String(answer.type ?? "").toLowerCase() === "success") {
      finish(answer, {}, method, report);
      return;
    }
    report({
      phase: "failed",
      message: String(answer.error ?? answer.message ?? "the provider refused the key").slice(0, 300),
    });
    return;
  }

  if (typeof raw.authorize !== "function") {
    // A key method: the operator pastes the credential, and the plugin's own
    // parser decides whether it is usable. No browser step exists.
    const inputs = argument.inputs && typeof argument.inputs === "object" ? argument.inputs : {};
    const pasted = String(inputs.key ?? inputs.api_key ?? inputs.token ?? "").trim();
    if (!pasted) {
      report({
        phase: "waiting",
        provider: providerID(hooks),
        method: method.id,
        mode: "key",
        url: "",
        instructions: "Paste the provider's API key.",
      });
      return;
    }
    finish({ type: "success", access: pasted, refresh: "", expires: 0 }, {}, method, report);
    return;
  }

  const inputs = argument.inputs && typeof argument.inputs === "object" ? argument.inputs : {};
  const answer = await raw.authorize(
    Object.fromEntries(Object.entries(inputs).map(([key, value]) => [key, String(value)])),
  );
  if (!answer || typeof answer !== "object") fail("the sign-in method answered nothing");
  const mode = answer.method === "code" ? "code" : "auto";
  // The page gets the URL as soon as the flow exists; the credential follows when
  // the plugin's own callback reports it.
  report({
    phase: "waiting",
    provider: providerID(hooks),
    method: method.id,
    mode,
    base_url: providerBaseURL(hooks, raw),
    url: String(answer.url ?? "").slice(0, 2000),
    instructions: String(answer.instructions ?? "").slice(0, 400),
  });
  if (typeof answer.callback !== "function") {
    report({ phase: "failed", message: "this sign-in method cannot finish on its own" });
    return;
  }
  const result = mode === "code" ? await answer.callback(String(argument.code ?? "")) : await answer.callback();
  finish(result, answer, method, report);

  fail(`unknown action: ${action}`);
}

// finish reports the credential a completed sign-in produced.
function finish(result, answer, method, report) {
  const status = String(result?.type ?? result?.status ?? "").toLowerCase();
  if (status !== "success") {
    report({
      phase: "failed",
      message: String(result?.error ?? result?.message ?? "the sign-in did not finish").slice(0, 300),
    });
    return;
  }
  const saved = captured.value && typeof captured.value === "object" ? captured.value : {};
  const credential = {
    access: String(result.access ?? result.key ?? saved.access ?? saved.key ?? ""),
    refresh: String(result.refresh ?? saved.refresh ?? ""),
    expires: Number(result.expires ?? saved.expires ?? 0) || 0,
    account_id: String(result.accountId ?? saved.accountId ?? "").slice(0, 200),
    // The plugin's own saved state is what its later calls need; the host stores it
    // as the channel credential, never on stdout of a probe.
    state: saved,
    method: method.id,
  };
  if (!credential.access) {
    report({ phase: "failed", message: "the sign-in produced no credential" });
    return;
  }
  report({ phase: "success", credential });
}

const timeout = setTimeout(() => fail("the sign-in timed out inside the sidecar"), 120000);
timeout.unref?.();

try {
  await run();
} catch (error) {
  fail(error?.message ?? error);
}
