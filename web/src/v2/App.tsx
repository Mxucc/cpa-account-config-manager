import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity,
  BadgeCheck,
  Boxes,
  CircleDollarSign,
  FileCog,
  Gauge,
  KeyRound,
  Route,
  ScrollText,
  Workflow,
} from "lucide-react";
import { readPanelAuth } from "../store/panelAuth";
import { clearSession, getSession, setSession } from "../store/session";
import { Button, Card, Field, Input, Notice, cn } from "../ui/kit";
import { ProviderAPIError, hasSession, providerApi } from "./api";
import { useCopy } from "./shared";
import { OverviewPage } from "./pages/Overview";
import { AccountsPage } from "./pages/Accounts";
import { ProvidersPage, type ProviderTab } from "./pages/Providers";
import { QuotaPage } from "./pages/Quota";
import { UsagePage } from "./pages/Usage";
import { RoutingPage } from "./pages/Routing";
import { AutomationPage } from "./pages/Automation";
import { SettingsPage } from "./pages/Settings";

type View = "overview" | "accounts" | "providers" | "quota" | "usage" | "routing" | "automation" | "settings";

interface Route {
  view: View;
  providerID?: string;
  providerTab?: ProviderTab;
  accountID?: string;
}

const VIEWS: View[] = ["overview", "accounts", "providers", "quota", "usage", "routing", "automation", "settings"];

// Hash routing keeps deep links working inside the Management Center iframe and
// survives a refresh without a server route.
export function parseRoute(hash: string): Route {
  const clean = hash.replace(/^#\/?/, "");
  const parts = clean.split("/").filter(Boolean);
  const [first, second, third, fourth] = parts;
  if (first === "providers") {
    return {
      view: "providers",
      providerID: second,
      providerTab: isProviderTab(third) ? third : undefined,
    };
  }
  if (first === "accounts" && second === "selected") {
    return { view: "accounts", accountID: decodeURIComponent(third ?? "") };
  }
  if (VIEWS.includes(first as View)) return { view: first as View };
  void fourth;
  return { view: "overview" };
}

function isProviderTab(value?: string): value is ProviderTab {
  return value === "accounts" || value === "quota" || value === "models" || value === "routing" || value === "advanced";
}

export function V2App() {
  const copy = useCopy();
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  const [authed, setAuthed] = useState(() => hasSession());
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [refreshRevision, setRefreshRevision] = useState(0);

  const navigate = useCallback((hash: string) => {
    window.location.hash = hash.startsWith("#") ? hash : `#${hash}`;
  }, []);

  useEffect(() => {
    const onHashChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  useEffect(() => {
    if (getSession()) return;
    // The Management Center writes its session on its own schedule: the frame can
    // mount while the center is still restoring one, so the first read is retried
    // for a moment instead of showing a login form the operator does not need.
    const adopt = () => {
      const panel = readPanelAuth({ allowStandalone: true });
      if (!panel) return false;
      setSession(panel.apiBase, panel.managementKey);
      setAuthed(true);
      return true;
    };
    if (adopt()) return;
    let attempts = 0;
    const timer = window.setInterval(() => {
      attempts += 1;
      if (adopt() || attempts >= 6) window.clearInterval(timer);
    }, 500);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 4000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  const handleAPIError = useCallback((caught: unknown) => {
    if (caught instanceof ProviderAPIError && caught.status === 401) {
      clearSession();
      setAuthed(false);
      setError(copy.auth.rejected);
      return;
    }
    setError(caught instanceof Error ? caught.message : String(caught));
  }, [copy.auth.rejected]);

  const onRefreshNeeded = useCallback(() => setRefreshRevision((value) => value + 1), []);

  const navItems = useMemo(
    () =>
      [
        { id: "overview" as View, label: copy.nav.overview, icon: Gauge },
        { id: "accounts" as View, label: copy.nav.accounts, icon: FileCog },
        { id: "providers" as View, label: copy.nav.providers, icon: Boxes },
        { id: "quota" as View, label: copy.nav.quota, icon: BadgeCheck },
        { id: "usage" as View, label: copy.nav.usage, icon: CircleDollarSign },
        { id: "routing" as View, label: copy.nav.routing, icon: Route },
        { id: "automation" as View, label: copy.nav.automation, icon: Workflow },
        { id: "settings" as View, label: copy.nav.settings, icon: ScrollText },
      ],
    [copy],
  );

  if (!authed) {
    return <LoginScreen error={error} onError={setError} onSignedIn={() => setAuthed(true)} />;
  }

  return (
    <div className="v2-shell min-h-screen bg-slate-50 text-gray-900">
      <div className="mx-auto flex min-h-screen w-full max-w-[1600px] flex-col lg:flex-row">
        <aside className="border-b border-gray-200 bg-white lg:w-56 lg:shrink-0 lg:border-b-0 lg:border-r">
          <div className="flex items-center gap-2.5 px-3 py-3.5">
            <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-blue-600 text-white shadow-sm">
              <Activity size={18} aria-hidden="true" />
            </span>
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold text-gray-900">CPA Account Manager</p>
              <p className="truncate text-xs text-gray-500">providers · quota · billing</p>
            </div>
          </div>
          <nav className="flex gap-0.5 overflow-x-auto px-2 pb-3 lg:flex-col lg:overflow-visible lg:pb-4" aria-label={copy.nav.overview}>
            {navItems.map((item) => {
              const Icon = item.icon;
              const active = route.view === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  aria-current={active ? "page" : undefined}
                  onClick={() => navigate(`/${item.id}`)}
                  className={cn(
                    "v2-nav-item shrink-0",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 focus-visible:ring-offset-2",
                  )}
                >
                  <span className="v2-nav-icon">
                    <Icon size={15} aria-hidden="true" />
                  </span>
                  <span className="whitespace-nowrap">{item.label}</span>
                </button>
              );
            })}
          </nav>
          <div className="hidden px-4 pb-4 lg:block">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                clearSession();
                setAuthed(false);
              }}
            >
              <KeyRound size={14} /> {copy.auth.signOut}
            </Button>
          </div>
        </aside>

        <main className="mx-auto w-full min-w-0 max-w-[1180px] flex-1 px-4 py-5 md:px-8 md:py-6">
          <div className="v2-page">
          {error ? <Notice tone="error">{error}</Notice> : null}
          {route.view === "overview" ? <OverviewPage onOpenAccount={(id) => navigate(`/accounts/selected/${encodeURIComponent(id)}`)} /> : null}
          {route.view === "accounts" ? (
            <AccountsPage
              selectedID={route.accountID}
              onSelect={(id) => navigate(id ? `/accounts/selected/${encodeURIComponent(id)}` : "/accounts")}
              onRefreshNeeded={onRefreshNeeded}
              onNotice={setNotice}
            />
          ) : null}
          {route.view === "providers" ? (
            <ProvidersPage
              providerID={route.providerID}
              tab={route.providerTab}
              onNavigate={navigate}
              onAPIError={handleAPIError}
              onNotice={setNotice}
              refreshRevision={refreshRevision}
            />
          ) : null}
          {route.view === "quota" ? <QuotaPage /> : null}
          {route.view === "usage" ? <UsagePage /> : null}
          {route.view === "routing" ? <RoutingPage /> : null}
          {route.view === "automation" ? (
            <AutomationPage refreshRevision={refreshRevision} onAPIError={handleAPIError} onNotice={setNotice} />
          ) : null}
          {route.view === "settings" ? <SettingsPage onAPIError={handleAPIError} onNotice={setNotice} /> : null}
          </div>
        </main>
      </div>
      {notice ? (
        <div className="v2-notice fixed bottom-5 right-5 z-50 max-w-sm bg-white shadow-md" role="status">
          {notice}
        </div>
      ) : null}
    </div>
  );
}

function LoginScreen({ error, onError, onSignedIn }: { error: string; onError: (message: string) => void; onSignedIn: () => void }) {
  const copy = useCopy();
  const [baseURL, setBaseURL] = useState(window.location.origin);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!key.trim()) return;
    setBusy(true);
    onError("");
    setSession(baseURL, key);
    try {
      await providerApi.providers();
      onSignedIn();
    } catch (caught) {
      clearSession();
      onError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 px-4">
      <Card className="w-full max-w-md">
        <div className="flex items-center gap-3">
          <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-600 text-white shadow-sm">
            <KeyRound size={18} aria-hidden="true" />
          </span>
          <div>
            <h1 className="font-semibold tracking-tight text-xl text-gray-900">
              {copy.auth.title}
            </h1>
            <p className="text-sm text-gray-500">CPA Account Config Manager</p>
          </div>
        </div>
        <form className="mt-6 space-y-4" onSubmit={submit}>
          <label className="block text-sm font-medium text-gray-700">
            {copy.auth.baseURL}
            <Input className="mt-1" value={baseURL} onChange={(event) => setBaseURL(event.target.value)} autoComplete="url" />
          </label>
          <label className="block text-sm font-medium text-gray-700">
            {copy.auth.key}
            <Input
              className="mt-1"
              type="password"
              value={key}
              onChange={(event) => setKey(event.target.value)}
              autoComplete="current-password"
            />
          </label>
          {error ? <p className="text-sm text-red-600">{error}</p> : null}
          <Button type="submit" loading={busy} className="w-full">
            {copy.auth.signIn}
          </Button>
        </form>
      </Card>
    </div>
  );
}
