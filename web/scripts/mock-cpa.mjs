import http from "node:http";

const port = Number(process.env.MOCK_CPA_PORT || 8318);
const managementKey = process.env.MOCK_CPA_KEY || "demo-key";
const previews = new Map();
let activeJob = null;
const importPreviews = new Map();
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

function textDownload(response, body, contentType, filename, headers = {}) {
  response.writeHead(200, {
    "Content-Type": contentType,
    "Content-Disposition": `attachment; filename="${filename}"`,
    "X-Content-Type-Options": "nosniff",
    ...headers,
  });
  response.end(body);
}

function csvDocument(headers, rows) {
  const cell = (raw) => {
    let value = raw === undefined || raw === null ? "" : String(raw);
    if (/^[\s]*[=+\-@]/.test(value)) value = `'${value}`;
    return `"${value.replaceAll('"', '""')}"`;
  };
  return [headers, ...rows].map((row) => row.map(cell).join(",")).join("\n") + "\n";
}

const zipCrc32Table = (() => {
  const table = new Uint32Array(256);
  for (let index = 0; index < table.length; index += 1) {
    let value = index;
    for (let bit = 0; bit < 8; bit += 1) value = (value & 1) ? (0xedb88320 ^ (value >>> 1)) : (value >>> 1);
    table[index] = value >>> 0;
  }
  return table;
})();

function zipCrc32(bytes) {
  let crc = 0xffffffff;
  bytes.forEach((byte) => {
    crc = zipCrc32Table[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  });
  return (crc ^ 0xffffffff) >>> 0;
}

function createStoredZip(entries, now = new Date()) {
  const chunks = [];
  const centralDirectory = [];
  const year = Math.max(1980, now.getFullYear());
  const dosTime = (now.getHours() << 11) | (now.getMinutes() << 5) | Math.floor(now.getSeconds() / 2);
  const dosDate = ((year - 1980) << 9) | ((now.getMonth() + 1) << 5) | now.getDate();
  let offset = 0;

  entries.forEach((entry) => {
    const name = Buffer.from(entry.name, "utf8");
    const data = Buffer.from(entry.content, "utf8");
    const crc = zipCrc32(data);
    const local = Buffer.alloc(30 + name.length);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(dosTime, 10);
    local.writeUInt16LE(dosDate, 12);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    name.copy(local, 30);

    const central = Buffer.alloc(46 + name.length);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(dosTime, 12);
    central.writeUInt16LE(dosDate, 14);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    name.copy(central, 46);

    chunks.push(local, data);
    centralDirectory.push(central);
    offset += local.length + data.length;
  });

  const centralSize = centralDirectory.reduce((size, entry) => size + entry.length, 0);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(centralSize, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...chunks, ...centralDirectory, end]);
}

function mockCPAAuth(account, index) {
  const base = {
    type: account.provider,
    name: account.label,
    email: account.email,
    disabled: account.disabled,
    priority: account.priority,
    note: account.note,
  };
  if (account.provider !== "codex") return { ...base, api_key: `demo-${account.provider}-key-${index + 1}` };
  return {
    ...base,
    type: "codex",
    account_id: `demo-account-${index + 1}`,
    chatgpt_account_id: `demo-account-${index + 1}`,
    plan_type: index % 2 ? "team" : "plus",
    access_token: `demo-access-token-${index + 1}`,
    refresh_token: index % 3 ? `demo-refresh-token-${index + 1}` : "",
    id_token: `demo.id-token-${index + 1}.signature`,
    session_token: `demo-session-token-${index + 1}`,
    last_refresh: new Date().toISOString(),
    expired: new Date(Date.now() + 24 * 60 * 60_000).toISOString(),
  };
}

function mockCredentialRecord(account, index) {
  const cpa = mockCPAAuth(account, index);
  return {
    cpa,
    name: account.label,
    email: account.email,
    accountId: cpa.account_id,
    planType: cpa.plan_type,
    accessToken: cpa.access_token,
    refreshToken: cpa.refresh_token,
    idToken: cpa.id_token,
    expiresAt: cpa.expired,
    lastRefresh: cpa.last_refresh,
  };
}

function mockCredentialDocument(format, records) {
  const oneOrMany = (items) => items.length === 1 ? items[0] : items;
  if (format === "sub2api") {
    return {
      exported_at: new Date().toISOString(),
      proxies: [],
      accounts: records.map((record) => ({
        name: record.name,
        platform: "openai",
        type: "oauth",
        concurrency: 10,
        priority: 1,
        credentials: {
          access_token: record.accessToken,
          chatgpt_account_id: record.accountId,
          email: record.email,
          expires_at: record.refreshToken ? undefined : record.expiresAt,
          plan_type: record.planType,
        },
        extra: { email: record.email, name: record.name, source: "cpa", last_refresh: record.lastRefresh },
      })),
    };
  }
  if (format === "cockpit") return oneOrMany(records.map((record) => ({
    type: "codex", id_token: record.idToken, access_token: record.accessToken, refresh_token: record.refreshToken,
    account_id: record.accountId, last_refresh: record.lastRefresh, email: record.email, expired: record.expiresAt,
  })));
  if (format === "9router") return oneOrMany(records.map((record) => ({
    accessToken: record.accessToken, refreshToken: record.refreshToken, expiresAt: record.expiresAt,
    providerSpecificData: { chatgptAccountId: record.accountId, chatgptPlanType: record.planType },
    id: record.accountId, provider: "codex", authType: "oauth", name: record.name, email: record.email,
    priority: 9, isActive: true, createdAt: record.lastRefresh, updatedAt: record.lastRefresh, testStatus: "active",
  })));
  if (format === "codex") return oneOrMany(records.map((record) => ({
    auth_mode: "chatgpt", OPENAI_API_KEY: null,
    tokens: { id_token: record.idToken, access_token: record.accessToken, refresh_token: record.refreshToken, account_id: record.accountId },
    last_refresh: record.lastRefresh,
  })));
  if (format === "axonhub") return oneOrMany(records.map((record) => ({
    auth_mode: "chatgpt", last_refresh: record.lastRefresh,
    tokens: { access_token: record.accessToken, refresh_token: record.refreshToken || "__missing_refresh_token__", id_token: record.idToken },
    ...(record.refreshToken ? {} : { axonhub_refresh_token_placeholder: true }),
  })));
  return oneOrMany(records.map((record) => ({
    tokens: { access_token: record.accessToken, refresh_token: record.refreshToken, id_token: record.idToken, account_id: record.accountId, chatgpt_account_id: record.accountId },
    meta: { label: record.name, chatgpt_account_id: record.accountId, note: "Exported from CPA Account Config Manager" },
  })));
}

function mockCredentialStem(value, fallback) {
  const stem = String(value || "").trim().toLowerCase().replace(/[^a-z0-9@._+-]+/g, "-").replace(/^[.\-_]+|[.\-_]+$/g, "").slice(0, 96);
  return stem || fallback;
}

function mockCredentialHeaders(exported, skipped) {
  return {
    "Cache-Control": "no-store, private, max-age=0",
    Pragma: "no-cache",
    Expires: "0",
    "Referrer-Policy": "no-referrer",
    "X-Exported-Accounts": String(exported),
    "X-Skipped-Accounts": String(skipped),
    "Access-Control-Expose-Headers": "Content-Disposition, X-Exported-Accounts, X-Skipped-Accounts",
  };
}

function mockAccountCount(value) {
  if (Array.isArray(value)) return value.reduce((total, item) => total + mockAccountCount(item), 0);
  if (!value || typeof value !== "object") return 0;
  if (value.accessToken || value.access_token || value.tokens?.access_token || value.credentials?.access_token) return 1;
  return Object.values(value).reduce((total, item) => total + mockAccountCount(item), 0);
}

async function mockTextImportCount(file) {
  const content = (await file.text()).trim();
  if (!content) return 0;
  try {
    return Math.max(1, mockAccountCount(JSON.parse(content)));
  } catch {
    const documents = content.split(/\r?\n/).filter((line) => line.trim()).map((line) => JSON.parse(line));
    return documents.reduce((total, document) => total + Math.max(1, mockAccountCount(document)), 0);
  }
}

function mockImportType(file) {
  const name = file.name.toLowerCase();
  if (name.endsWith(".zip")) return "zip";
  if (name.endsWith(".txt") || name.endsWith(".jsonl") || name.endsWith(".ndjson")) return "text";
  return "json";
}

function authorized(request) {
  return request.headers.authorization === `Bearer ${managementKey}`;
}

async function readJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};
}

async function readFormData(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  const body = Buffer.concat(chunks);
  const webRequest = new Request("http://127.0.0.1/import", {
    method: "POST",
    headers: request.headers,
    body,
  });
  return webRequest.formData();
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

  if (request.method === "GET" && url.pathname.endsWith("/plugins/cpa-account-config-manager/accounts")) {
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
    const format = url.searchParams.get("format") || "";
    const view = listFromURL(url);
    const supported = new Set(["cpa", "sub2api", "cockpit", "9router", "codex", "axonhub", "codexmanager"]);
    if (!supported.has(format)) return json(response, 400, { error: "请选择账号导出目标格式" });
    const fileAccounts = view.accounts.filter((account) => !account.runtime_only && account.source === "file");
    if (format === "cpa") {
      if (!fileAccounts.length) return json(response, 422, { error: "当前筛选没有可导出的文件账号" });
      const documents = fileAccounts.map((account, index) => ({ account, content: JSON.stringify(mockCPAAuth(account, index), null, 2) + "\n" }));
      const headers = mockCredentialHeaders(documents.length, view.accounts.length - documents.length);
      if (documents.length === 1) {
        return textDownload(response, documents[0].content, "application/json; charset=utf-8", `${mockCredentialStem(documents[0].account.email, "account-001")}.json`, headers);
      }
      const used = new Set();
      const entries = documents.map(({ account, content }, index) => {
        const stem = mockCredentialStem(account.email, `account-${String(index + 1).padStart(3, "0")}`);
        let candidate = stem;
        let suffix = 1;
        while (used.has(`${candidate}.json`)) {
          suffix += 1;
          candidate = `${stem}-${suffix}`;
        }
        used.add(`${candidate}.json`);
        return { name: `${candidate}.json`, content };
      });
      return textDownload(response, createStoredZip(entries), "application/zip", "cpa-accounts.zip", headers);
    }
    const compatible = fileAccounts.filter((account) => account.provider === "codex");
    if (!compatible.length) return json(response, 422, { error: "当前筛选没有兼容的 Codex OAuth 账号" });
    const records = compatible.map(mockCredentialRecord);
    const body = JSON.stringify(mockCredentialDocument(format, records), null, 2) + "\n";
    const suffix = format === "codexmanager" ? "codex-manager" : format;
    const filename = format === "codex" && records.length === 1 ? "auth.json" : `cpa-accounts.${suffix}.json`;
    return textDownload(response, body, "application/json; charset=utf-8", filename, mockCredentialHeaders(records.length, view.accounts.length - records.length));
  }
  if (request.method === "GET" && url.pathname.endsWith("/export/results")) {
    const format = url.searchParams.get("format") || "json";
    const snapshot = snapshotJob(true);
    const results = snapshot.results || [];
    if (format === "csv") {
      const headers = ["job_id", "job_state", "id", "name", "provider", "label", "status", "error", "applied_fields", "retryable"];
      const rows = results.map((result) => [snapshot.id, snapshot.state, result.id, result.name, result.provider, result.label, result.status, result.error, result.applied_fields?.join(";"), result.retryable]);
      return textDownload(response, csvDocument(headers, rows), "text/csv; charset=utf-8", "demo-results.csv");
    }
    if (format === "jsonl" || format === "ndjson") {
      const body = results.map((result) => JSON.stringify({ job_id: snapshot.id, job_state: snapshot.state, ...result })).join("\n") + (results.length ? "\n" : "");
      return textDownload(response, body, "application/x-ndjson; charset=utf-8", "demo-results.jsonl");
    }
    return json(response, 200, snapshot, { "Content-Disposition": 'attachment; filename="demo-results.json"', "X-Content-Type-Options": "nosniff" });
  }
  if (request.method === "POST" && url.pathname.endsWith("/import/preview")) {
    const formData = await readFormData(request);
    const files = formData.getAll("files").filter((file) => typeof file?.name === "string");
    if (!files.length) return json(response, 400, { error: "multipart import contains no files" });
    const previewID = crypto.randomUUID();
    const items = [];
    let sourceFiles = 0;
    for (const [fileIndex, file] of files.entries()) {
      const isZip = file.name.toLowerCase().endsWith(".zip");
      const count = isZip ? 2 : await mockTextImportCount(file);
      sourceFiles += isZip ? count : 1;
      for (let entryIndex = 0; entryIndex < count; entryIndex += 1) {
        const sequence = items.length + 1;
        const stem = file.name.replace(/\.[^.]+$/, "").replace(/[^a-z0-9]+/gi, "_").toLowerCase() || `import_${sequence}`;
        const email = `${stem}_${entryIndex + 1}@example.com`;
        items.push({
          index: sequence,
          source_name: isZip ? `${file.name}/account-${entryIndex + 1}.json` : file.name,
          source_path: isZip ? `$[${entryIndex}]` : count > 1 ? `$document[${entryIndex}]` : "$",
          target_name: `codex-${stem}_${entryIndex + 1}.json`,
          email,
          account_id: `demo-import-${fileIndex + 1}-${entryIndex + 1}`,
          label: email,
          synthetic_id_token: true,
          warnings: ["ID token was synthesized from account metadata"],
        });
      }
    }
    const inputTypes = new Set(files.map(mockImportType));
    const preview = {
      id: previewID,
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      input_type: inputTypes.size > 1 ? "mixed" : [...inputTypes][0],
      source_files: sourceFiles,
      total: items.length,
      skipped: 0,
      warnings: ["existing Auth files will not be overwritten"],
      items,
    };
    importPreviews.set(previewID, preview);
    return json(response, 200, preview);
  }
  if (request.method === "POST" && url.pathname.endsWith("/import/start")) {
    const body = await readJSON(request);
    const preview = importPreviews.get(body.preview_id);
    if (!preview) return json(response, 404, { error: "import preview not found" });
    const results = preview.items.map((item) => {
      accounts.push({
        id: `auth-import-${crypto.randomUUID()}`,
        auth_id: `runtime-${item.account_id}`,
        name: item.target_name,
        provider: "codex",
        type: "codex",
        label: item.label,
        email: item.email,
        account_type: "oauth",
        status: "active",
        status_message: "",
        disabled: false,
        unavailable: false,
        runtime_only: false,
        source: "file",
        priority: 0,
        note: "Imported by mock CPA",
        prefix: "",
        proxy: "",
        proxy_configured: false,
        websockets: false,
        header_names: [],
        header_count: 0,
        editable: true,
        read_only_reason: "",
        success: 0,
        failed: 0,
        updated_at: new Date().toISOString(),
      });
      return { ...item, status: "imported" };
    });
    importPreviews.delete(body.preview_id);
    return json(response, 200, {
      id: preview.id,
      state: "completed",
      total: results.length,
      imported: results.length,
      skipped: 0,
      failed: 0,
      started_at: new Date().toISOString(),
      finished_at: new Date().toISOString(),
      results,
    });
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
