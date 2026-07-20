import http from "node:http";

const port = Number(process.env.MOCK_CPA_PORT || 8318);
const managementKey = process.env.MOCK_CPA_KEY || "demo-key";
const previews = new Map();
const deletePreviews = new Map();
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
let inspectionPolicy = {
  enabled: true,
  scan_interval_minutes: 30,
  model_probe_enabled: true,
  model_probe_full_sweep: true,
  scan_manually_disabled: false,
  model_probe_interval_minutes: 60,
  model_probe_batch_size: 20,
  model_probe_models: {
    codex: "gpt-5.4",
    openai: "gpt-5.4",
    claude: "claude-sonnet-4-5-20250929",
    gemini: "gemini-2.0-flash",
    xai: "grok-4",
  },
  failure_threshold: 3,
  recovery_threshold: 2,
  passive_circuit_enabled: true,
  passive_failure_threshold: 5,
  passive_failure_window_minutes: 180,
  passive_circuit_minutes: 15,
  auto_disable: true,
  auto_enable: true,
  auto_delete: false,
  auto_delete_invalid_credentials: false,
  delete_grace_hours: 168,
  delete_batch_size: 10,
  anomaly_trigger_enabled: true,
  anomaly_threshold_percent: 50,
  anomaly_minimum_accounts: 10,
  anomaly_cooldown_minutes: 60,
};
let updatePolicy = {
  check_enabled: true,
  check_interval_hours: 24,
  auto_update: false,
};

const operationLog = Array.from({ length: 18 }, (_, index) => {
  const variants = [
    ["inspection", "inspection_scan", index % 5 === 0 ? "partial" : "succeeded", "inspection"],
    ["inspection", "auto_disable", "succeeded", "inspection"],
    ["default_policy", "policy_scan", "succeeded", "default_policy"],
    ["import", "import", "succeeded", "import"],
    ["export", "export_accounts", "succeeded", "manual"],
    ["update", "update_check", "warning", "background"],
  ];
  const [category, action, status, source] = variants[index % variants.length];
  const started = new Date(Date.now() - index * 47 * 60_000);
  return {
    id: `demo-operation-${index + 1}`,
    event_id: `demo-event-${index + 1}`,
    category,
    action,
    status,
    source,
    scope: category === "inspection" ? "scheduled" : category === "export" ? "filtered" : "system",
    target_id: action === "auto_disable" ? `auth-${String((index % 12) + 1).padStart(3, "0")}` : undefined,
    target_count: action === "inspection_scan" ? 12 : 1,
    succeeded: status === "succeeded" ? (action === "inspection_scan" ? 12 : 1) : 0,
    failed: status === "partial" ? 1 : 0,
    skipped: 0,
    started_at: started.toISOString(),
    finished_at: new Date(started.getTime() + 850).toISOString(),
    reason_code: status === "warning" ? "update_available" : status === "partial" ? "partial_failure" : "completed",
    version: category === "update" ? "0.3.0" : undefined,
    format: category === "export" ? "cpa" : undefined,
  };
});

const providers = ["codex", "claude", "gemini", "antigravity"];
const planTypes = ["free", "plus", "pro", "team", "business", "enterprise", "edu", "k12"];

function mockAutomation(index, disabled) {
  if (!disabled) return undefined;
  const base = {
    last_checked_at: new Date(Date.now() - 2 * 60_000).toISOString(),
    owned_disable: index !== 0,
    auto_disable_eligible: index !== 0,
    inspection_enabled: true,
    auto_disable_enabled: true,
    auto_enable_enabled: true,
    auto_delete_enabled: false,
    failure_threshold: 3,
    failure_streak: 3,
    recovery_threshold: 2,
    healthy_streak: 0,
  };
  if (index === 7) return {
    ...base,
    health: "quota_limited",
    reason_code: "quota_exhausted",
    recommendation: "enable",
    disable_reason: "quota_exhausted",
    disabled_at: new Date(Date.now() - 90 * 60_000).toISOString(),
    recover_after: new Date(Date.now() + 38 * 60_000).toISOString(),
    auto_action: "disable",
    auto_action_status: "succeeded",
  };
  if (index === 14) return {
    ...base,
    health: "deactivated",
    reason_code: "workspace_deactivated",
    recommendation: "delete",
    disable_reason: "workspace_deactivated",
    disabled_at: new Date(Date.now() - 48 * 60 * 60_000).toISOString(),
  };
  if (index === 21) return {
    ...base,
    health: "invalid_credentials",
    reason_code: "invalid_credentials",
    recommendation: "reauth",
    disable_reason: "invalid_credentials",
    disabled_at: new Date(Date.now() - 4 * 60 * 60_000).toISOString(),
  };
  if (index === 28) return {
    ...base,
    health: "quota_limited",
    reason_code: "quota_exhausted",
    recommendation: "enable",
    disable_reason: "quota_exhausted",
    disabled_at: new Date(Date.now() - 3 * 60 * 60_000).toISOString(),
    recover_after: new Date(Date.now() + (2 * 24 + 5) * 60 * 60_000).toISOString(),
    auto_action: "disable",
    auto_action_status: "succeeded",
  };
  if (index === 35) return {
    ...base,
    health: "unavailable",
    reason_code: "invalid_response",
    recommendation: "disable",
    disable_reason: "passive_circuit_open",
    disabled_at: new Date(Date.now() - 4 * 60_000).toISOString(),
    recover_after: new Date(Date.now() + 11 * 60_000).toISOString(),
    passive_circuit_enabled: true,
    passive_failure_threshold: 5,
    passive_failure_streak: 5,
    circuit_open: true,
    circuit_reason_code: "invalid_response",
    auto_action: "disable",
    auto_action_status: "succeeded",
  };
  return {
    ...base,
    health: "disabled",
    reason_code: "manual_disabled",
    recommendation: "review",
    owned_disable: false,
    auto_disable_eligible: false,
    failure_streak: 0,
  };
}

const accounts = Array.from({ length: 36 }, (_, index) => {
  const provider = providers[index % providers.length];
  const readOnly = index % 11 === 0;
  const disabled = index % 7 === 0;
  const automation = mockAutomation(index, disabled);
  const recentRequests = Array.from({ length: 6 }, (_, bucket) => ({
    time: new Date(Date.now() - (5 - bucket) * 10 * 60_000).toISOString(),
    success: (index + bucket * 2) % 7,
    failed: (index + bucket) % 9 === 0 ? 1 : 0,
  }));
  const hasUsage = index % 10 !== 9;
  const hasCodexQuota = provider === "codex" && index % 8 !== 0;
  const usage = hasUsage ? {
    input_tokens: 120_000 + index * 18_750,
    output_tokens: 34_000 + index * 4_200,
    reasoning_tokens: index % 3 === 0 ? 8_000 + index * 750 : 0,
    cached_tokens: index % 2 === 0 ? 21_000 + index * 600 : 0,
    cache_read_tokens: index % 2 === 0 ? 18_000 + index * 500 : 0,
    cache_creation_tokens: index % 5 === 0 ? 3_000 : 0,
    total_tokens: 162_000 + index * 23_700,
    last_request_at: new Date(Date.now() - (index % 7) * 7 * 60_000).toISOString(),
    updated_at: new Date(Date.now() - (index % 7) * 7 * 60_000).toISOString(),
    ...(hasCodexQuota ? {
      codex: {
        observed_at: new Date(Date.now() - 2 * 60_000).toISOString(),
        five_hour: {
          used_percent: index === 12 ? 118 : 18 + (index % 6) * 12.5,
          reset_at: new Date(Date.now() + 38 * 60_000).toISOString(),
          window_minutes: 300,
        },
        seven_day: {
          used_percent: 31 + (index % 5) * 14,
          reset_at: new Date(Date.now() + 4 * 24 * 60 * 60_000).toISOString(),
          window_minutes: 10_080,
        },
      },
    } : {}),
  } : undefined;
  return {
    id: `auth-${String(index + 1).padStart(3, "0")}`,
    auth_id: `runtime-${index + 1}`,
    name: `${provider}-${String(index + 1).padStart(2, "0")}.json`,
    provider,
    type: provider,
    label: `operator-${String(index + 1).padStart(2, "0")}@example.com`,
    email: `operator-${String(index + 1).padStart(2, "0")}@example.com`,
    account_type: index % 3 === 0 ? "oauth" : "api_key",
    plan_type: provider === "codex" ? planTypes[Math.floor(index / providers.length) % planTypes.length] : undefined,
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
    recent_requests: recentRequests,
    next_retry_after: index % 9 === 0 ? new Date(Date.now() + 12 * 60_000).toISOString() : undefined,
    ...(automation ? { automation } : {}),
    ...(usage ? { usage } : {}),
    updated_at: new Date(Date.now() - index * 43 * 60_000).toISOString(),
  };
});

function mockInspectionResults() {
  const checkedAt = new Date().toISOString();
  const states = [
    ["deactivated", "workspace_deactivated", "high", "delete", true],
    ["invalid_credentials", "invalid_credentials", "high", "reauth", true],
    ["quota_limited", "quota_exhausted", "high", "disable", true],
    ["review", "authentication_review", "medium", "review", false],
    ["unavailable", "native_unavailable", "medium", "review", false],
  ];
  return accounts.slice(0, 12).map((account, index) => {
    const state = states[index] || ["healthy", "healthy_recent_success", "high", "keep", false];
    const circuit = index === 4;
    return {
      id: account.id,
      name: account.name,
      provider: account.provider,
      type: account.account_type,
      plan_type: account.plan_type,
      health: circuit ? "unavailable" : state[0],
      reason_code: circuit ? "invalid_response" : state[1],
      confidence: state[2],
      recommendation: circuit ? "disable" : state[3],
      disabled: circuit || account.disabled,
      editable: account.editable,
      auto_disable_eligible: state[4],
      owned_disable: circuit,
      failure_streak: circuit ? 5 : state[4] ? Math.min(5, index + 1) : 0,
      healthy_streak: state[0] === "healthy" ? index - 3 : 0,
      probe_status: index < 5 ? "unavailable" : "available",
      probe_reason_code: index < 2 ? "authentication_failed" : index === 2 ? "quota_limited" : index < 5 ? "upstream_unavailable" : "model_response_ok",
      probe_model: account.provider === "gemini" ? "gemini-2.0-flash" : "gpt-5.4",
      probe_tested_at: checkedAt,
      probe_latency_ms: 180 + index * 17,
      signal_source: index < 5 ? "active_probe" : "native",
      circuit_open: circuit,
      circuit_reason_code: circuit ? "invalid_response" : undefined,
      recover_after: circuit ? new Date(Date.now() + 11 * 60_000).toISOString() : undefined,
      last_checked_at: checkedAt,
    };
  });
}

function mockInspectionSnapshot(pending = false) {
  const results = mockInspectionResults();
  const count = (health) => results.filter((result) => result.health === health).length;
  return {
    policy: inspectionPolicy,
    running: false,
    pending,
    last_run: {
      scanned: results.length,
      healthy: count("healthy"),
      quota_limited: count("quota_limited"),
      invalid_credentials: count("invalid_credentials"),
      deactivated: count("deactivated"),
      review: count("review"),
      unavailable: count("unavailable"),
      disabled: count("disabled"),
      unknown: count("unknown"),
      auto_disabled: 0,
      auto_enabled: 0,
      delete_pending: 0,
      failed: 0,
      truncated: 0,
      started_at: new Date(Date.now() - 850).toISOString(),
      finished_at: new Date().toISOString(),
    },
    total: results.length,
    action_count: 3,
    active_probe_armed: true,
    last_native_run_at: new Date().toISOString(),
    last_probe_run_at: new Date(Date.now() - 60_000).toISOString(),
    probe_sweep_remaining: 7,
    probe_sweep_total: 12,
    probe_sweep_completed: 5,
    probe_sweep_source: "manual",
    probe_sweep_status: "running",
    probe_sweep_started_at: new Date(Date.now() - 35_000).toISOString(),
    anomaly_eligible: 10,
    anomaly_count: 5,
    anomaly_percent: 50,
    anomaly_trigger_pending: false,
    last_anomaly_trigger_at: new Date(Date.now() - 90_000).toISOString(),
  };
}

function mockUpdateSnapshot(pending = false) {
  return {
    policy: updatePolicy,
    current_version: "0.2.0",
    update_available: false,
    checking: false,
    pending,
    checked_at: new Date().toISOString(),
    error: "release metadata request failed",
  };
}

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
    plan_type: account.plan_type || (index % 2 ? "team" : "plus"),
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
    if (filters.type && ![account.plan_type, account.account_type, account.type].includes(filters.type)) return false;
    if (filters.status && account.status !== filters.status) return false;
    if (filters.disabled !== undefined && account.disabled !== filters.disabled) return false;
    if (filters.editability === "editable" && !account.editable) return false;
    if ((filters.editability === "read_only" || filters.editability === "readonly") && account.editable) return false;
    if (filters.search) {
      const search = String(filters.search).toLowerCase();
      if (!`${account.id}\n${account.name}\n${account.label}\n${account.provider}\n${account.plan_type}\n${account.account_type}\n${account.note}`.toLowerCase().includes(search)) return false;
    }
    return true;
  });
}

function listFromURL(url) {
  const filters = {};
  for (const key of ["provider", "type", "status", "editability", "search"]) {
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

function upsertMockOperation(eventID, operation) {
  const index = operationLog.findIndex((entry) => entry.event_id === eventID);
  const next = {
    id: index >= 0 ? operationLog[index].id : crypto.randomUUID(),
    event_id: eventID,
    target_count: 0,
    succeeded: 0,
    failed: 0,
    skipped: 0,
    started_at: new Date().toISOString(),
    ...operation,
  };
  if (index >= 0) operationLog[index] = next;
  else operationLog.push(next);
  if (operationLog.length > 2000) operationLog.splice(0, operationLog.length - 2000);
  return next;
}

function mockJobOperation(snapshot, action, category = "batch") {
  if (!snapshot.id) return;
  const status = snapshot.running ? "running" : snapshot.state === "completed" ? "succeeded" : snapshot.state;
  upsertMockOperation(`${category}:${snapshot.id}`, {
    category,
    action,
    status,
    source: "manual",
    scope: category === "batch" ? "selected" : "all",
    target_count: snapshot.total,
    succeeded: snapshot.succeeded,
    failed: snapshot.failed + snapshot.conflicts,
    skipped: snapshot.skipped,
    started_at: snapshot.started_at,
    finished_at: snapshot.finished_at,
    reason_code: snapshot.running ? undefined : status === "succeeded" ? "completed" : "partial_failure",
    related_job_id: snapshot.id,
  });
}

function filteredMockOperations(url) {
  mockJobOperation(snapshotJob(false), "batch_edit");
  mockJobOperation(snapshotForceJob(false), "force_sync", "default_policy");
  const category = url.searchParams.get("category") || "";
  const status = url.searchParams.get("status") || "";
  const source = url.searchParams.get("source") || "";
  const search = (url.searchParams.get("search") || "").trim().toLowerCase();
  return operationLog.filter((operation) => {
    if (category && operation.category !== category) return false;
    if (status && operation.status !== status) return false;
    if (source && operation.source !== source) return false;
    if (!search) return true;
    return [operation.id, operation.category, operation.action, operation.status, operation.source, operation.scope, operation.target_id, operation.reason_code, operation.related_job_id, operation.version, operation.format, operation.model]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(search));
  }).sort((left, right) => new Date(right.finished_at || right.started_at) - new Date(left.finished_at || left.started_at));
}

function mockOperationSummary(operations) {
  return operations.reduce((summary, operation) => {
    summary.total += 1;
    if (operation.status === "running") summary.running += 1;
    else if (operation.status === "succeeded") summary.succeeded += 1;
    else if (operation.status === "failed") summary.failed += 1;
    else if (operation.status === "interrupted") summary.interrupted += 1;
    else summary.attention += 1;
    return summary;
  }, { total: 0, running: 0, succeeded: 0, failed: 0, attention: 0, interrupted: 0 });
}

function operationCSV(operations) {
  const headers = ["id", "category", "action", "status", "source", "scope", "target_id", "target_count", "succeeded", "failed", "skipped", "started_at", "finished_at", "reason_code", "related_job_id", "related_action_id", "version", "format", "model"];
  const rows = operations.map((entry) => headers.map((key) => entry[key]));
  return csvDocument(headers, rows);
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url || "/", `http://${request.headers.host}`);
  if (!authorized(request)) return json(response, 401, { error: "invalid management key" });

  if (request.method === "GET" && url.pathname.endsWith("/operations/export")) {
    const format = url.searchParams.get("format") || "json";
    const operations = filteredMockOperations(url);
    const headers = { "X-Exported-Operations": String(operations.length), "Access-Control-Expose-Headers": "Content-Disposition, X-Exported-Operations" };
    if (format === "csv") return textDownload(response, operationCSV(operations), "text/csv; charset=utf-8", "demo-operations.csv", headers);
    if (format === "jsonl") return textDownload(response, operations.map((entry) => JSON.stringify(entry)).join("\n") + (operations.length ? "\n" : ""), "application/x-ndjson; charset=utf-8", "demo-operations.jsonl", headers);
    return textDownload(response, JSON.stringify({ exported_at: new Date().toISOString(), count: operations.length, operations }, null, 2) + "\n", "application/json; charset=utf-8", "demo-operations.json", headers);
  }
  if (request.method === "GET" && url.pathname.endsWith("/operations")) {
    const operations = filteredMockOperations(url);
    const page = Math.max(1, Number(url.searchParams.get("page")) || 1);
    const pageSize = Math.min(200, Math.max(1, Number(url.searchParams.get("page_size")) || 50));
    const start = (page - 1) * pageSize;
    return json(response, 200, {
      operations: operations.slice(start, start + pageSize),
      summary: mockOperationSummary(operations),
      total: operations.length,
      page,
      page_size: pageSize,
      pages: operations.length ? Math.ceil(operations.length / pageSize) : 0,
    });
  }
  if (request.method === "DELETE" && url.pathname.endsWith("/operations")) {
    operationLog.splice(0);
    const now = new Date().toISOString();
    const operation = upsertMockOperation(`journal-clear:${now}`, {
      category: "journal", action: "journal_clear", status: "succeeded", source: "manual", scope: "system",
      target_count: 0, succeeded: 0, failed: 0, skipped: 0, started_at: now, finished_at: now, reason_code: "completed",
    });
    return json(response, 200, { operation, retained: 1 });
  }
  if (request.method === "POST" && url.pathname.endsWith("/operations/record")) {
    const body = await readJSON(request);
    if (body.action !== "update_install" || !["succeeded", "failed", "warning"].includes(body.status)) return json(response, 400, { error: "unsupported operation record" });
    const now = new Date().toISOString();
    const operation = upsertMockOperation(`update-install:${crypto.randomUUID()}`, {
      category: "update", action: "update_install", status: body.status, source: "plugin_store", scope: "system",
      target_count: 0, succeeded: 0, failed: body.status === "failed" ? 1 : 0, skipped: 0, started_at: now, finished_at: now,
      reason_code: body.status === "warning" ? "restart_required" : body.status === "failed" ? "install_failed" : "completed", version: body.version,
    });
    return json(response, 201, operation);
  }

  if (request.method === "GET" && url.pathname.endsWith("/plugins/cpa-account-config-manager/accounts")) {
    return json(response, 200, listFromURL(url));
  }
  if (request.method === "POST" && url.pathname.endsWith("/accounts/model-test")) {
    const body = await readJSON(request);
    const account = accounts.find((candidate) => candidate.id === body.account_id);
    if (!account) return json(response, 404, { error: "account was not found" });
    const defaults = { codex: "gpt-5.4", openai: "gpt-5.4", claude: "claude-sonnet-4-5-20250929", gemini: "gemini-2.0-flash", aistudio: "gemini-2.0-flash", xai: "grok-4" };
    const model = String(body.model || defaults[account.provider] || "").trim();
    const supported = ["codex", "openai", "claude", "gemini", "gemini-cli", "gemini-interactions", "aistudio", "xai"].includes(account.provider);
    const now = new Date().toISOString();
    const result = {
      account_id: account.id,
      provider: account.provider,
      model,
      status: supported ? "available" : "unsupported",
      reason_code: supported ? "model_response_ok" : "unsupported_provider",
      latency_ms: supported ? 286 : 0,
      tested_at: now,
    };
    upsertMockOperation(`model-test:${crypto.randomUUID()}`, {
      category: "account", action: "model_test", status: supported ? "succeeded" : "skipped", source: "manual", scope: "single",
      target_id: account.id, target_count: 1, succeeded: supported ? 1 : 0, failed: 0, skipped: supported ? 0 : 1,
      started_at: now, finished_at: now, reason_code: result.reason_code, model,
    });
    return json(response, 200, result);
  }
  if (request.method === "POST" && url.pathname.endsWith("/accounts/delete/preview")) {
    const body = await readJSON(request);
    const account = accounts.find((candidate) => candidate.id === body.id);
    if (!account) return json(response, 404, { error: "account was not found" });
    if (!account.editable || account.runtime_only || account.source !== "file") {
      return json(response, 400, { error: "account is read-only and cannot be deleted" });
    }
    const previewID = crypto.randomUUID();
    const preview = {
      id: previewID,
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      account: {
        id: account.id,
        name: account.name,
        provider: account.provider,
        type: account.type,
        plan_type: account.plan_type,
        label: account.label,
        email: account.email,
        status: account.status,
        source: account.source,
      },
    };
    deletePreviews.set(previewID, { accountID: account.id, name: account.name, preview });
    return json(response, 200, preview);
  }
  if (request.method === "POST" && url.pathname.endsWith("/accounts/delete/start")) {
    const body = await readJSON(request);
    const stored = deletePreviews.get(body.preview_id);
    if (!stored) return json(response, 404, { error: "delete preview not found" });
    const accountIndex = accounts.findIndex((candidate) => candidate.id === stored.accountID && candidate.name === stored.name);
    if (accountIndex < 0 || !accounts[accountIndex].editable) {
      return json(response, 409, { error: "account changed after delete preview" });
    }
    accounts.splice(accountIndex, 1);
    deletePreviews.delete(body.preview_id);
    return json(response, 200, {
      status: "deleted",
      deleted_at: new Date().toISOString(),
      account: stored.preview.account,
    });
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
  if ((request.method === "GET" || request.method === "POST") && url.pathname.endsWith("/export/accounts")) {
    const format = url.searchParams.get("format") || "";
    let view = listFromURL(url);
    if (request.method === "POST") {
      const body = await readJSON(request);
      if (body.scope?.mode !== "selected" || !Array.isArray(body.scope.ids) || body.scope.ids.length === 0) {
        return json(response, 400, { error: "selected scope requires at least one account id" });
      }
      const ids = new Set(body.scope.ids);
      view = { accounts: accounts.filter((account) => ids.has(account.id)), total: ids.size };
    }
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
        plan_type: "k12",
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
  if (request.method === "PATCH" && url.pathname.endsWith("/plugins/cpa-account-config-manager/config")) {
    const body = await readJSON(request);
    if (body.default_policy) {
      defaultPolicy = {
        enabled: Boolean(body.default_policy.enabled),
        apply_mode: "missing",
        scan_interval_seconds: Math.min(300, Math.max(5, Number(body.default_policy.scan_interval_seconds) || 15)),
        priority: body.default_policy.priority === null ? null : Number(body.default_policy.priority),
        websockets: body.default_policy.websockets === null ? null : Boolean(body.default_policy.websockets),
      };
    }
    return json(response, 200, { status: "ok" });
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
  if (request.method === "GET" && url.pathname.endsWith("/inspection/results")) {
    const health = url.searchParams.get("health") || "";
    const search = (url.searchParams.get("search") || "").toLowerCase();
    const page = Math.max(1, Number(url.searchParams.get("page")) || 1);
    const pageSize = Math.min(200, Math.max(1, Number(url.searchParams.get("page_size")) || 50));
    const filtered = mockInspectionResults().filter((result) => {
      if (health && result.health !== health) return false;
      if (!search) return true;
      return [result.id, result.name, result.provider, result.type, result.plan_type, result.reason_code]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(search));
    });
    const start = (page - 1) * pageSize;
    return json(response, 200, {
      results: filtered.slice(start, start + pageSize),
      total: filtered.length,
      page,
      page_size: pageSize,
      pages: filtered.length ? Math.ceil(filtered.length / pageSize) : 0,
    });
  }
  if (request.method === "GET" && url.pathname.endsWith("/inspection/actions")) {
    const results = mockInspectionResults();
    const createdAt = new Date().toISOString();
    return json(response, 200, {
      actions: [
        { id: "demo-action-disable", account_id: results[1].id, name: results[1].name, provider: results[1].provider, action: "disable", status: "succeeded", reason_code: results[1].reason_code, created_at: createdAt },
        { id: "demo-action-enable", account_id: results[5].id, name: results[5].name, provider: results[5].provider, action: "enable", status: "succeeded", reason_code: "healthy_recent_success", created_at: createdAt },
        { id: "demo-action-delete", account_id: results[0].id, name: results[0].name, provider: results[0].provider, action: "delete_candidate", status: "pending", reason_code: results[0].reason_code, created_at: createdAt },
      ],
    });
  }
  if (request.method === "POST" && url.pathname.endsWith("/inspection/scan")) {
    return json(response, 202, mockInspectionSnapshot(true));
  }
  if (request.method === "POST" && url.pathname.endsWith("/inspection/scan/native")) {
    return json(response, 202, mockInspectionSnapshot(true));
  }
  if (request.method === "POST" && url.pathname.endsWith("/inspection/auto-delete")) {
    return json(response, 200, { attempted: 0, succeeded: 0, failed: 0, skipped: 0 });
  }
  if (request.method === "PUT" && url.pathname.endsWith("/inspection")) {
    const body = await readJSON(request);
    inspectionPolicy = {
      enabled: Boolean(body.enabled),
      scan_interval_minutes: Math.min(1440, Math.max(5, Number(body.scan_interval_minutes) || 30)),
      model_probe_enabled: Boolean(body.model_probe_enabled),
      model_probe_full_sweep: Boolean(body.model_probe_full_sweep),
      scan_manually_disabled: Boolean(body.scan_manually_disabled),
      model_probe_interval_minutes: Math.min(1440, Math.max(5, Number(body.model_probe_interval_minutes) || 60)),
      model_probe_batch_size: Math.min(200, Math.max(1, Number(body.model_probe_batch_size) || 20)),
      model_probe_models: {
        codex: String(body.model_probe_models?.codex || "gpt-5.4"),
        openai: String(body.model_probe_models?.openai || "gpt-5.4"),
        claude: String(body.model_probe_models?.claude || "claude-sonnet-4-5-20250929"),
        gemini: String(body.model_probe_models?.gemini || "gemini-2.0-flash"),
        xai: String(body.model_probe_models?.xai || "grok-4"),
      },
      failure_threshold: Math.min(10, Math.max(2, Number(body.failure_threshold) || 3)),
      recovery_threshold: Math.min(10, Math.max(1, Number(body.recovery_threshold) || 2)),
      passive_circuit_enabled: Boolean(body.passive_circuit_enabled),
      passive_failure_threshold: Math.min(100, Math.max(2, Number(body.passive_failure_threshold) || 5)),
      passive_failure_window_minutes: Math.min(1440, Math.max(1, Number(body.passive_failure_window_minutes) || 180)),
      passive_circuit_minutes: Math.min(1440, Math.max(1, Number(body.passive_circuit_minutes) || 15)),
      auto_disable: Boolean(body.auto_disable),
      auto_enable: Boolean(body.auto_enable),
      auto_delete: Boolean(body.auto_delete),
      auto_delete_invalid_credentials: Boolean(body.auto_delete_invalid_credentials),
      delete_grace_hours: Math.min(8760, Math.max(24, Number(body.delete_grace_hours) || 168)),
      delete_batch_size: Math.min(100, Math.max(1, Number(body.delete_batch_size) || 10)),
      anomaly_trigger_enabled: Boolean(body.anomaly_trigger_enabled),
      anomaly_threshold_percent: Math.min(100, Math.max(1, Number(body.anomaly_threshold_percent) || 50)),
      anomaly_minimum_accounts: Math.min(10_000, Math.max(1, Number(body.anomaly_minimum_accounts) || 10)),
      anomaly_cooldown_minutes: Math.min(1440, Math.max(5, Number(body.anomaly_cooldown_minutes) || 60)),
    };
    return json(response, 200, mockInspectionSnapshot());
  }
  if (request.method === "GET" && url.pathname.endsWith("/inspection")) {
    return json(response, 200, mockInspectionSnapshot());
  }
  if (request.method === "POST" && url.pathname.endsWith("/updates/check")) {
    return json(response, 202, mockUpdateSnapshot(true));
  }
  if (request.method === "PUT" && url.pathname.endsWith("/updates")) {
    const body = await readJSON(request);
    updatePolicy = {
      check_enabled: Boolean(body.policy?.check_enabled),
      check_interval_hours: Math.min(168, Math.max(1, Number(body.policy?.check_interval_hours) || 24)),
      auto_update: Boolean(body.policy?.auto_update),
    };
    return json(response, 200, mockUpdateSnapshot());
  }
  if (request.method === "GET" && url.pathname.endsWith("/updates")) {
    return json(response, 200, mockUpdateSnapshot());
  }
  if (request.method === "GET" && url.pathname === "/v0/management/plugin-store") {
    return json(response, 200, {
      plugins_enabled: true,
      plugins: [{ id: "cpa-account-config-manager", version: "0.3.0", installed: true, installed_version: "0.2.0", update_available: true }],
    });
  }
  if (request.method === "POST" && url.pathname === "/v0/management/plugin-store/cpa-account-config-manager/install") {
    return json(response, 200, { status: "installed", id: "cpa-account-config-manager", version: "0.3.0", restart_required: true });
  }
  return json(response, 404, { error: "not found" });
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`Mock CPA listening on http://127.0.0.1:${port} (key: ${managementKey})\n`);
});
