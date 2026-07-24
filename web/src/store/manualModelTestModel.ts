export const DEFAULT_MANUAL_MODEL_TEST_MODEL = "gpt-5.6-sol";
export const MANUAL_MODEL_TEST_STORAGE_KEY = "cpa-account-config-manager:manual-model-test-model";

const MAX_MODEL_ID_LENGTH = 128;
const MAX_PROVIDER_ID_LENGTH = 64;
const MAX_TESTED_MODELS_PER_PROVIDER = 100;
const STORAGE_VERSION = 1;

interface StoredProviderModels {
  last_model: string;
  tested_models: string[];
}

interface StoredManualModelPreferences {
  version: number;
  providers: Record<string, StoredProviderModels>;
}

export interface ManualModelTestPreference {
  model: string;
  testedModels: string[];
}

export function normalizeManualModelTestModel(value: unknown): string {
  if (typeof value !== "string") return "";
  const normalized = value.replace(/[\u0000-\u001f\u007f]/g, "").trim();
  if (normalized.length === 0 || normalized.length > MAX_MODEL_ID_LENGTH) return "";
  return normalized;
}

function normalizeProvider(value: unknown): string {
  if (typeof value !== "string") return "default";
  const normalized = value.replace(/[\u0000-\u001f\u007f]/g, "").trim().toLowerCase();
  if (normalized.length === 0 || normalized.length > MAX_PROVIDER_ID_LENGTH) return "default";
  return `provider:${normalized === "codex-agent-identity" ? "codex" : normalized}`;
}

function emptyPreferences(): StoredManualModelPreferences {
  return { version: STORAGE_VERSION, providers: {} };
}

function readStoredPreferences(): StoredManualModelPreferences {
  if (typeof window === "undefined") return emptyPreferences();
  try {
    const raw = window.localStorage.getItem(MANUAL_MODEL_TEST_STORAGE_KEY);
    if (!raw) return emptyPreferences();
    const parsed = JSON.parse(raw) as Partial<StoredManualModelPreferences>;
    if (parsed.version !== STORAGE_VERSION || !parsed.providers || typeof parsed.providers !== "object" || Array.isArray(parsed.providers)) {
      return emptyPreferences();
    }
    return { version: STORAGE_VERSION, providers: parsed.providers as Record<string, StoredProviderModels> };
  } catch {
    return emptyPreferences();
  }
}

function validStoredModels(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const unique = new Set<string>();
  for (const candidate of value) {
    const model = normalizeManualModelTestModel(candidate);
    if (model) unique.add(model);
    if (unique.size >= MAX_TESTED_MODELS_PER_PROVIDER) break;
  }
  return [...unique];
}

export function readManualModelTestPreference(provider: string, fallbackModel = DEFAULT_MANUAL_MODEL_TEST_MODEL): ManualModelTestPreference {
  const preferences = readStoredPreferences();
  const stored = preferences.providers[normalizeProvider(provider)];
  const lastModel = normalizeManualModelTestModel(stored?.last_model);
  const storedModels = validStoredModels(stored?.tested_models);
  const testedModels = lastModel
    ? [lastModel, ...storedModels.filter((candidate) => candidate !== lastModel)]
    : storedModels;
  const fallback = normalizeManualModelTestModel(fallbackModel) || DEFAULT_MANUAL_MODEL_TEST_MODEL;
  return {
    model: lastModel || testedModels[0] || fallback,
    testedModels,
  };
}

export function recordManualModelTestModel(provider: string, value: string): ManualModelTestPreference {
  const model = normalizeManualModelTestModel(value);
  const providerKey = normalizeProvider(provider);
  const current = readStoredPreferences();
  const previousModels = validStoredModels(current.providers[providerKey]?.tested_models);
  const testedModels = model
    ? [model, ...previousModels.filter((candidate) => candidate !== model)].slice(0, MAX_TESTED_MODELS_PER_PROVIDER)
    : previousModels;

  if (model && typeof window !== "undefined") {
    try {
      current.providers[providerKey] = { last_model: model, tested_models: testedModels };
      window.localStorage.setItem(MANUAL_MODEL_TEST_STORAGE_KEY, JSON.stringify(current));
    } catch {
      // The active model test still proceeds when browser storage is unavailable.
    }
  }

  return {
    model: model || previousModels[0] || DEFAULT_MANUAL_MODEL_TEST_MODEL,
    testedModels,
  };
}
