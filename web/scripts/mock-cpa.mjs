import http from "node:http";

const port = Number(process.env.MOCK_CPA_PORT || 8318);
const managementKey = process.env.MOCK_CPA_KEY || "demo-key";
const previews = new Map();
let activeJob = null;
const forcePreviews = new Map();
let activeForceJob = null;
let defaultPolicy = {
  enabled: false,
  apply_mode: "missing",
  scan_interval_seconds: 15,
  priority: null,
  websockets: null,
};
let lastPolicyScan = {
  scanned: 0,
  eligible: 0,
  changed: 0,
  skipped: 0,
  failed: 0,
};

const providers = ["codex", "claude", "gemini", "antigravity"];
const accounts = Array.from({ length: 36 }, (_, index) => {
  const provider = providers[index % providers.length];
  const readOnly = index % 11 === 0;
  const disabled = index % 7 === 0;
  return {
    id: `auth-${String(index + 1).padStart(3, "0")}`,
    auth_id: `runtime-${index + 1}`,
    name: `${provider}-${String(index + 1).padStart(2, "0")}.json`,
    provider,
    type: provider,
    label: `operator-${String(index + 1).padStart(2, "0")}@example.com`,
    email: `operator-${String(index + 1).padStart(2, "0")}@example.com`,
    account_type: index % 3 === 0 ? "oauth" : "api_key",
    status: disabled ? "disabled" : index % 9 === 0 ? "error" : "active",
    status_message: index % 9 === 0 ? "upstream temporarily unavailable" : "",
    disabled,
    unavailable: index % 9 === 0,
    runtime_only: readOnly,
    source: readOnly ? "runtime" : "file",
    priority: (index % 8) - 2,
    note: index % 5 === 0 ? "primary pool" : "",
    prefix: index % 4 === 0 ? "team-a" : "",
    proxy: index % 6 === 0 ? "http://127.0.0.1:7890" : "",
    proxy_configured: index % 6 === 0,
    websockets: index % 3 === 0,
    header_names: index % 4 === 0 ? ["Authorization", "X-Team"] : [],
    header_count: index % 4 === 0 ? 2 : 0,
    editable: !readOnly,
    read_only_reason: readOnly ? "runtime-only account has no physical auth file" : "",
    success: 80 + index * 3,
    failed: index % 6,
    updated_at: new Date(Date.now() - index * 43 * 60_000).toISOString(),
  };
});

function json(response, status, body, headers = {}) {
  response.writeHead(status, { "Content-Type": "application/json; charset=utf-8", ...headers });
  response.end(JSON.stringify(body));
}

function authorized(request) {
  return request.headers.authorization === `Bearer ${managementKey}`;
}

async function readJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};
}

function filterAccounts(filters) {
  return accounts.filter((account) => {
    if (filters.provider && account.provider !== filters.provider) return false;
    if (filters.status && account.status !== filters.status) return false;
    if (filters.disabled !== undefined && account.disabled !== filters.disabled) return false;
    if (filters.editability === "editable" && !account.editable) return false;
    if ((filters.editability === "read_only" || filters.editability === "readonly") && account.editable) return false;
    if (filters.search) {
      const search = String(filters.search).toLowerCase();
      if (!`${account.id}\n${account.name}\n${account.label}\n${account.provider}\n${account.note}`.toLowerCase().includes(search)) return false;
    }
    return true;
  });
}

function listFromURL(url) {
  const filters = {};
  for (const key of ["provider", "status", "editability", "search"]) {
    if (url.searchParams.get(key)) filters[key] = url.searchParams.get(key);
  }
  if (url.searchParams.has("disabled")) filters.disabled = url.searchParams.get("disabled") === "true";
  const filtered = filterAccounts(filters);
  const page = Math.max(1, Number(url.searchParams.get("page") || 1));
  const pageSize = Math.max(1, Number(url.searchParams.get("page_size") || 50));
  return {
    accounts: filtered.slice((page - 1) * pageSize, page * pageSize),
    total: filtered.length,
    page,
    page_size: pageSize,
    pages: Math.ceil(filtered.length / pageSize),
  };
}

function resolveScope(scope) {
  if (scope.mode === "selected") {
    const ids = new Set(scope.ids || []);
    return accounts.filter((account) => ids.has(account.id));
  }
  return filterAccounts(scope.filters || {});
}

function snapshotJob(includeResults = true) {
  if (!activeJob) {
    return {
      state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
      failed: 0, conflicts: 0, skipped: 0, workers: 0,
      patch: { fields: [], proxy_mutation: false }, retry_available: false, persisted: false,
    };
  }
  const elapsed = Date.now() - activeJob.started;
  const done = Math.min(activeJob.targets.length, Math.floor(elapsed / 260));
  const running = done < activeJob.targets.length;
  const results = activeJob.targets.map((target, index) => ({
    id: target.id,
    name: target.name,
    provider: target.provider,
    label: target.label,
    status: index < done ? "succeeded" : index === done && running ? "running" : "pending",
    applied_fields: index < done ? activeJob.fields : [],
    retryable: false,
  }));
  return {
    id: activeJob.id,
    state: running ? "running" : "completed",
    running,
    total: activeJob.targets.length,
    eligible: activeJob.targets.length,
    done,
    succeeded: done,
    failed: 0,
    conflicts: 0,
    skipped: 0,
    workers: 6,
    patch: { fields: activeJob.fields, proxy_mutation: activeJob.fields.includes("proxy_url") },
    started_at: new Date(activeJob.started).toISOString(),
    finished_at: running ? undefined : new Date().toISOString(),
    retry_available: false,
    persisted: !running,
    ...(includeResults ? { results } : {}),
  };
}

function forcePolicySummary() {
  const fields = [];
  if (defaultPolicy.priority !== null) fields.push("priority");
  if (defaultPolicy.websockets !== null) fields.push("websockets");
  return { fields, priority: defaultPolicy.priority, websockets: defaultPolicy.websockets };
}

function snapshotForceJob(includeResults = true) {
  if (!activeForceJob) {
    return {
      state: "idle", running: false, total: 0, eligible: 0, done: 0, succeeded: 0,
      failed: 0, conflicts: 0, skipped: 0, workers: 0,
      policy: { fields: [], priority: null, websockets: null },
    };
  }
  const elapsed = Date.now() - activeForceJob.started;
  const done = Math.min(activeForceJob.targets.length, Math.floor(elapsed / 230));
  const running = done < activeForceJob.targets.length;
  const results = activeForceJob.targets.map((target, index) => ({
    id: target.id,
    name: target.name,
    provider: target.provider,
    label: target.label,
    status: index < done ? "succeeded" : index === done && running ? "running" : "pending",
    applied_fields: index < done ? activeForceJob.policy.fields : [],
    retryable: false,
  }));
  if (!running && !activeForceJob.applied) {
    activeForceJob.applied = true;
    for (const target of activeForceJob.targets) {
      if (activeForceJob.policy.priority !== null) target.priority = activeForceJob.policy.priority;
      if (activeForceJob.policy.websockets !== null) target.websockets = activeForceJob.policy.websockets;
    }
  }
  return {
    id: activeForceJob.id,
    state: running ? "running" : "completed",
    running,
    total: activeForceJob.targets.length,
    eligible: activeForceJob.targets.length,
    done,
    succeeded: done,
    failed: 0,
    conflicts: 0,
    skipped: 0,
    workers: 6,
    policy: activeForceJob.policy,
    started_at: new Date(activeForceJob.started).toISOString(),
    finished_at: running ? undefined : new Date().toISOString(),
    ...(includeResults ? { results } : {}),
  };
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url || "/", `http://${request.headers.host}`);
  if (!authorized(request)) return json(response, 401, { error: "invalid management key" });

  if (request.method === "GET" && url.pathname.endsWith("/accounts")) {
    return json(response, 200, listFromURL(url));
  }
  if (request.method === "POST" && url.pathname.endsWith("/batch/preview")) {
    const body = await readJSON(request);
    const targets = resolveScope(body.scope || {});
    const fields = Object.keys(body.patch || {});
    const previewID = crypto.randomUUID();
    const preview = {
      id: previewID,
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      scope_mode: body.scope?.mode || "filtered",
      total: targets.length,
      eligible: targets.filter((target) => target.editable).length,
      read_only: targets.filter((target) => !target.editable).length,
      missing: 0,
      physical_files: targets.filter((target) => target.editable).length,
      providers: Object.fromEntries(providers.map((provider) => [provider, targets.filter((target) => target.provider === provider).length]).filter(([, count]) => count > 0)),
      patch: {
        fields,
        header_set: Object.keys(body.patch?.headers?.set || {}),
        header_remove: body.patch?.headers?.remove || [],
        proxy_mutation: fields.includes("proxy_url"),
      },
      warnings: targets.some((target) => !target.editable) ? [`${targets.filter((target) => !target.editable).length} target(s) are read-only and will be skipped`] : [],
      targets: targets.map((target) => ({ id: target.id, name: target.name, provider: target.provider, label: target.label, eligible: target.editable, read_only_reason: target.read_only_reason })),
    };
    previews.set(previewID, { preview, targets: targets.filter((target) => target.editable), fields });
    return json(response, 200, preview);
  }
  if (request.method === "POST" && url.pathname.endsWith("/batch/start")) {
    const body = await readJSON(request);
    const stored = previews.get(body.preview_id);
    if (!stored) return json(response, 404, { error: "preview not found" });
    activeJob = { id: crypto.randomUUID(), started: Date.now(), targets: stored.targets, fields: stored.fields };
    return json(response, 202, snapshotJob(true));
  }
  if (request.method === "GET" && url.pathname.endsWith("/batch/status")) {
    return json(response, 200, snapshotJob(url.searchParams.get("light") !== "1"));
  }
  if (request.method === "POST" && url.pathname.endsWith("/batch/retry")) {
    return json(response, 400, { error: "no failed targets are available to retry" });
  }
  if (request.method === "GET" && url.pathname.endsWith("/export/accounts")) {
    return json(response, 200, { exported_at: new Date().toISOString(), ...listFromURL(url) }, { "Content-Disposition": 'attachment; filename="demo-accounts.json"' });
  }
  if (request.method === "GET" && url.pathname.endsWith("/export/results")) {
    return json(response, 200, snapshotJob(true), { "Content-Disposition": 'attachment; filename="demo-results.json"' });
  }
  if (request.method === "GET" && url.pathname.endsWith("/defaults")) {
    return json(response, 200, { policy: defaultPolicy, running: false, last_scan: lastPolicyScan });
  }
  if (request.method === "PUT" && url.pathname.endsWith("/defaults")) {
    const body = await readJSON(request);
    defaultPolicy = {
      enabled: Boolean(body.enabled),
      apply_mode: "missing",
      scan_interval_seconds: Math.min(300, Math.max(5, Number(body.scan_interval_seconds) || 15)),
      priority: body.priority === null ? null : Number(body.priority),
      websockets: body.websockets === null ? null : Boolean(body.websockets),
    };
    return json(response, 200, { policy: defaultPolicy, running: false, last_scan: lastPolicyScan });
  }
  if (request.method === "POST" && url.pathname.endsWith("/defaults/scan")) {
    const editable = accounts.filter((account) => account.editable);
    lastPolicyScan = {
      started_at: new Date().toISOString(),
      finished_at: new Date().toISOString(),
      scanned: accounts.length,
      eligible: editable.length,
      changed: 0,
      skipped: accounts.length,
      failed: 0,
    };
    return json(response, 202, { policy: defaultPolicy, running: false, last_scan: lastPolicyScan });
  }
  if (request.method === "POST" && url.pathname.endsWith("/defaults/force/preview")) {
    const policy = forcePolicySummary();
    if (policy.fields.length === 0) return json(response, 400, { error: "default policy does not manage any fields" });
    const previewID = crypto.randomUUID();
    const editable = accounts.filter((account) => account.editable);
    const readOnly = accounts.filter((account) => !account.editable);
    const preview = {
      id: previewID,
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      total: accounts.length,
      eligible: editable.length,
      read_only: readOnly.length,
      physical_files: editable.length,
      policy,
      warnings: readOnly.length ? [`${readOnly.length} target(s) are read-only and will be skipped`] : [],
      targets: accounts.map((target) => ({ id: target.id, name: target.name, provider: target.provider, label: target.label, eligible: target.editable, read_only_reason: target.read_only_reason })),
    };
    forcePreviews.set(previewID, { preview, targets: editable, policy });
    return json(response, 200, preview);
  }
  if (request.method === "POST" && url.pathname.endsWith("/defaults/force/start")) {
    const body = await readJSON(request);
    const stored = forcePreviews.get(body.preview_id);
    if (!stored) return json(response, 404, { error: "force-sync preview not found" });
    activeForceJob = { id: crypto.randomUUID(), started: Date.now(), targets: stored.targets, policy: stored.policy, applied: false };
    forcePreviews.delete(body.preview_id);
    return json(response, 202, snapshotForceJob(true));
  }
  if (request.method === "GET" && url.pathname.endsWith("/defaults/force/status")) {
    return json(response, 200, snapshotForceJob(url.searchParams.get("light") !== "1"));
  }
  return json(response, 404, { error: "not found" });
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`Mock CPA listening on http://127.0.0.1:${port} (key: ${managementKey})\n`);
});
