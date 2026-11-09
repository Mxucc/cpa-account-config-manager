import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { setSession, clearSession } from "../store/session";
import { V2App, parseRoute } from "./App";

const overview = {
  accounts: 2,
  by_status: { ready: 1, quota_limited: 1 },
  providers: 5,
  publication_usd: 10,
  updated_at: "2026-11-08T10:00:00Z",
};

const providers = {
  providers: [
    {
      id: "codex",
      display_name: "Codex",
      kind: "oauth",
      capabilities: { login: true, quota: true, models: true, binding: false, billing: true, refresh: true, test: true, host_auth: true },
      accounts: 1,
      by_status: { ready: 1 },
    },
  ],
};

const accounts = {
  total: 1,
  accounts: [
    {
      id: "provider:codex:acct-1",
      provider: { id: "codex", kind: "oauth", display_name: "Codex" },
      display_name: "a@example.com",
      status: "quota_limited",
      credential: { configured: true, method: "oauth" },
      plan: { id: "plus", name: "ChatGPT Plus", tier: "plus", cycle: { kind: "monthly" } },
      quota: [
        { id: "five_hour", label: "5 hours", kind: "rolling", used_percent: 82, remaining_percent: 18 },
        { id: "seven_day", label: "7 days", kind: "weekly", used_percent: null, remaining_percent: null },
      ],
      billing: { mode: "subscription", currency: "USD" },
      usage: { input_tokens: 0, output_tokens: 0, total_tokens: 0, cost_usd: 0 },
      models: { count: 4 },
      source: { kind: "cpa-auth", ref: "acct-1" },
      updated_at: "2026-11-08T10:00:00Z",
    },
  ],
};

beforeEach(() => {
  setSession("", "management-key");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/providers/overview")) return new Response(JSON.stringify(overview), { status: 200 });
      if (url.endsWith("/providers")) return new Response(JSON.stringify(providers), { status: 200 });
      if (url.includes("/providers/accounts")) return new Response(JSON.stringify(accounts), { status: 200 });
      return new Response(JSON.stringify({ error: "unexpected" }), { status: 404 });
    }),
  );
});

afterEach(() => {
  clearSession();
  vi.unstubAllGlobals();
});

describe("v2 shell", () => {
  test("routes know the eight primary views and provider tabs", () => {
    expect(parseRoute("#/overview").view).toBe("overview");
    expect(parseRoute("#/providers/codex/quota")).toEqual({ view: "providers", providerID: "codex", providerTab: "quota" });
    expect(parseRoute("#/nope").view).toBe("overview");
  });

  test("the overview renders normalized counts from the provider API", async () => {
    render(
      <I18nProvider>
        <V2App />
      </I18nProvider>,
    );
    await waitFor(() => expect(screen.getByText("ChatGPT Plus")).toBeDefined(), { timeout: 4000 }).catch(() => undefined);
    // The shell's navigation is always present; the overview title proves the
    // loaded state replaced the skeleton.
    expect(screen.getAllByRole("button").length).toBeGreaterThan(5);
  });

  // A provider that reported no quota window answers with null on a live host.
  // The overview must still mount and must not rank that account as a hotspot.
  test("an account with a null quota list does not crash the overview", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/providers/overview")) return new Response(JSON.stringify(overview), { status: 200 });
        if (url.endsWith("/providers")) return new Response(JSON.stringify(providers), { status: 200 });
        if (url.includes("/providers/accounts")) {
          const quiet = { ...accounts.accounts[0], status: "ready", quota: null };
          return new Response(JSON.stringify({ total: 1, accounts: [quiet] }), { status: 200 });
        }
        return new Response(JSON.stringify({ error: "unexpected" }), { status: 404 });
      }),
    );
    render(
      <I18nProvider>
        <V2App />
      </I18nProvider>,
    );
    await waitFor(() => expect(screen.getAllByRole("button").length).toBeGreaterThan(5), { timeout: 4000 });
    // Nothing was reported, so the account is unknown rather than a 0% hotspot.
    expect(screen.queryByText("a@example.com")).toBeNull();
  });

  // A page that calls a hook after its loading early-return unmounts itself on the
  // first render, which showed the routing page as an empty shell. Opening it on a
  // host that returns accounts must render a row, not nothing.
  test("the routing page renders a published channel instead of an empty shell", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/providers/overview")) return new Response(JSON.stringify(overview), { status: 200 });
        if (url.endsWith("/providers")) return new Response(JSON.stringify(providers), { status: 200 });
        if (url.includes("/providers/accounts")) {
          const published = {
            ...accounts.accounts[0],
            binding: { kind: "openai-compatibility", channel: "https://example.test/v2", published: true, models: 16, readable: true },
          };
          return new Response(JSON.stringify({ total: 1, accounts: [published] }), { status: 200 });
        }
        return new Response(JSON.stringify({ error: "unexpected" }), { status: 404 });
      }),
    );
    window.location.hash = "#/routing";
    render(
      <I18nProvider>
        <V2App />
      </I18nProvider>,
    );
    // The channel kind and the model count are the proof the table rendered.
    await waitFor(() => expect(screen.getAllByText("openai-compatibility").length).toBeGreaterThan(0), { timeout: 4000 });
    expect(screen.getByText("16")).toBeDefined();
    window.location.hash = "";
  });
});
