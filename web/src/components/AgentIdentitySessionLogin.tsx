import { CheckCircle2, ExternalLink, Eye, EyeOff, KeyRound, LoaderCircle, X } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import { readPanelAuth } from "../store/panelAuth";
import { clearSession, setSession } from "../store/session";
import type { AgentIdentitySessionLoginResponse, OpenCodeQuotaResult } from "../types";
import { LoginDialog } from "./LoginDialog";

interface AgentIdentitySessionLoginProps {
  loginState: string | null;
}

type AuthenticationState = "booting" | "login" | "ready";
type LoginMode = "gpt" | "opencode";

export function AgentIdentitySessionLogin({ loginState }: AgentIdentitySessionLoginProps) {
  const { locale, tx } = useI18n();
  const [authentication, setAuthentication] = useState<AuthenticationState>(loginState ? "booting" : "ready");
  const [authenticationLoading, setAuthenticationLoading] = useState(false);
  const [authenticationError, setAuthenticationError] = useState("");
  const [mode, setMode] = useState<LoginMode>("gpt");
  const [sessionJSON, setSessionJSON] = useState("");
  const [sessionVisible, setSessionVisible] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<AgentIdentitySessionLoginResponse | null>(null);
  const [opencodeWorkspace, setOpenCodeWorkspace] = useState("");
  const [opencodeCookie, setOpenCodeCookie] = useState("");
  const [opencodeVisible, setOpenCodeVisible] = useState(false);
  const [opencodeResult, setOpenCodeResult] = useState<{ account: { id: string; workspace_id: string }; result: OpenCodeQuotaResult } | null>(null);

  useEffect(() => {
    if (!loginState) return;
    let active = true;
    const bootstrap = async () => {
      const panelAuth = readPanelAuth({ allowStandalone: true });
      if (!panelAuth) {
        if (active) setAuthentication("login");
        return;
      }
      setSession(panelAuth.apiBase, panelAuth.managementKey);
      try {
        await api.verifySession();
        if (active) setAuthentication("ready");
      } catch {
        clearSession();
        if (active) setAuthentication("login");
      }
    };
    void bootstrap();
    return () => { active = false; };
  }, [loginState]);

  const authenticate = async (baseURL: string, managementKey: string) => {
    setAuthenticationLoading(true);
    setAuthenticationError("");
    setSession(baseURL, managementKey);
    try {
      await api.verifySession();
      setAuthentication("ready");
    } catch (caught) {
      clearSession();
      setAuthenticationError(caught instanceof Error ? operatorMessage(caught.message, locale) : tx("ui.authentication_failed"));
    } finally {
      setAuthenticationLoading(false);
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const submittedSession = sessionJSON.trim();
    if (!loginState || !submittedSession || submitting) return;
    setSessionJSON("");
    setSessionVisible(false);
    setSubmitting(true);
    setError("");
    try {
      setResult(await api.completeAgentIdentitySessionLogin(loginState, submittedSession));
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        clearSession();
        setAuthentication("login");
        setAuthenticationError(tx("ui.authentication_failed"));
      } else if (caught instanceof api.APIError && (caught.status === 404 || caught.status === 410)) {
        setError(tx("ui.agent_identity_login_expired"));
      } else {
        setError(operatorMessage(caught instanceof Error ? caught.message : "ui.operation_failed", locale));
      }
    } finally {
      setSubmitting(false);
    }
  };

  const submitOpenCode = async (event: FormEvent) => {
    event.preventDefault();
    const workspace = opencodeWorkspace.trim();
    const cookie = opencodeCookie.trim();
    if (!workspace || !cookie || submitting) return;
    setSubmitting(true);
    setError("");
    setOpenCodeResult(null);
    try {
      const response = await api.saveOpenCodeAccount(workspace, cookie);
      setOpenCodeWorkspace("");
      setOpenCodeCookie("");
      setOpenCodeVisible(false);
      setOpenCodeResult(response);
    } catch (caught) {
      if (caught instanceof api.APIError && caught.status === 401) {
        clearSession();
        setAuthentication("login");
        setAuthenticationError(tx("ui.authentication_failed"));
      } else {
        setError(operatorMessage(caught instanceof Error ? caught.message : "ui.operation_failed", locale));
      }
    } finally {
      setSubmitting(false);
    }
  };

  const closeWindow = () => window.close();

  if (!loginState) {
    return (
      <main className="agent-login-page">
        <section className="agent-login-panel" aria-labelledby="agent-login-title">
          <div className="agent-login-heading">
            <span className="agent-login-mark"><KeyRound size={22} /></span>
            <div><h1 id="agent-login-title">{tx("ui.agent_identity_session_login")}</h1><p>{tx("ui.invalid_agent_identity_login_state")}</p></div>
          </div>
          <div className="agent-login-error" role="alert">{tx("ui.agent_identity_login_expired")}</div>
          <div className="agent-login-actions"><button className="button" type="button" onClick={closeWindow}><X size={16} />{tx("ui.close_login_window")}</button></div>
        </section>
      </main>
    );
  }

  if (authentication === "booting") {
    return <div className="auth-loading" aria-label={tx("ui.converting_agent_identity")}><LoaderCircle className="spin" size={24} /></div>;
  }

  if (authentication === "login") {
    return <LoginDialog loading={authenticationLoading} error={authenticationError} onSubmit={authenticate} />;
  }

  const completed = result !== null || opencodeResult !== null;

  return (
    <main className="agent-login-page">
      <section className="agent-login-panel" aria-labelledby="agent-login-title">
        <div className="agent-login-heading">
          <span className={`agent-login-mark ${completed ? "is-complete" : ""}`}>{completed ? <CheckCircle2 size={22} /> : <KeyRound size={22} />}</span>
          <div>
            <h1 id="agent-login-title">{completed ? tx("ui.agent_identity_login_complete") : tx("ui.agent_identity_session_login")}</h1>
            <p>{result ? tx("ui.cpa_is_saving_agent_identity") : opencodeResult ? tx("ui.opencode_is_saved") : tx("ui.agent_identity_session_login_description")}</p>
          </div>
        </div>

        {!completed ? (
          <div className="agent-login-mode" role="tablist" aria-label={tx("ui.choose_login_method")}>
            <button className={mode === "gpt" ? "is-active" : ""} type="button" role="tab" aria-selected={mode === "gpt"} disabled={submitting} onClick={() => { setMode("gpt"); setError(""); }}>{tx("ui.gpt_login")}</button>
            <button className={mode === "opencode" ? "is-active" : ""} type="button" role="tab" aria-selected={mode === "opencode"} disabled={submitting} onClick={() => { setMode("opencode"); setError(""); }}>{tx("ui.opencode_login")}</button>
          </div>
        ) : null}

        {result ? (
          <div className="agent-login-result" aria-label={tx("ui.agent_identity_login_complete")}>
            <div><span>{tx("ui.accounts")}</span><strong>{result.account.email || tx("ui.unknown")}</strong></div>
            <div><span>{tx("ui.plan_type")}</span><strong>{result.account.plan_type || tx("ui.unknown")}</strong></div>
            <div><span>{tx("ui.provider")}</span><strong>{result.account.provider}</strong></div>
          </div>
        ) : opencodeResult ? (
          <div className="agent-login-result" aria-label={tx("ui.opencode_login_complete")}>
            <div><span>{tx("ui.opencode_workspace_id")}</span><strong>{opencodeResult.account.workspace_id}</strong></div>
            <div><span>{tx("ui.accounts")}</span><strong>{tx("ui.opencode_saved_account")}</strong></div>
            {opencodeResult.result.success ? (
              <>
                <div><span>{tx("ui.opencode_rolling")}</span><strong>{formatOpenCodeWindow(opencodeResult.result.rolling)}</strong></div>
                <div><span>{tx("ui.opencode_weekly")}</span><strong>{formatOpenCodeWindow(opencodeResult.result.weekly)}</strong></div>
                <div><span>{tx("ui.opencode_monthly")}</span><strong>{formatOpenCodeWindow(opencodeResult.result.monthly)}</strong></div>
              </>
            ) : (
              <div><span>{tx("ui.quota")}</span><strong>{opencodeResult.result.error || tx("ui.unknown")}</strong></div>
            )}
            <a className="agent-login-session-link opencode-status-link" href="/v0/resource/plugins/cpa-account-config-manager/opencode-status" target="_blank" rel="noopener noreferrer">
              <ExternalLink size={16} />{tx("ui.opencode_open_status")}
            </a>
          </div>
        ) : mode === "gpt" ? (
          <form className="agent-login-form" onSubmit={submit}>
            <a className="agent-login-session-link" href="https://chatgpt.com/api/auth/session" target="_blank" rel="noopener noreferrer">
              <ExternalLink size={16} />{tx("ui.open_chatgpt_session")}
            </a>
            <label className="field-block agent-session-field">
              <span>{tx("ui.chatgpt_session_json")}</span>
              <span className="agent-session-input">
                <textarea
                  className={sessionVisible ? "is-visible" : "is-masked"}
                  value={sessionJSON}
                  onChange={(event) => setSessionJSON(event.target.value)}
                  placeholder={tx("ui.chatgpt_session_json_placeholder")}
                  aria-label={tx("ui.chatgpt_session_json")}
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  disabled={submitting}
                  autoFocus
                />
                <button type="button" aria-label={tx(sessionVisible ? "ui.hide_session_json" : "ui.show_session_json")} title={tx(sessionVisible ? "ui.hide_session_json" : "ui.show_session_json")} onClick={() => setSessionVisible((visible) => !visible)}>
                  {sessionVisible ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </span>
            </label>
            <p className="agent-login-privacy">{tx("ui.session_json_privacy_notice")}</p>
            {error ? <div className="agent-login-error" role="alert">{error}</div> : null}
            <div className="agent-login-actions">
              <button className="button" type="button" onClick={closeWindow} disabled={submitting}><X size={16} />{tx("ui.cancel")}</button>
              <button className="button button-primary" type="submit" disabled={submitting || sessionJSON.trim() === ""}>
                {submitting ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}
                {tx(submitting ? "ui.converting_agent_identity" : "ui.convert_and_login")}
              </button>
            </div>
          </form>
        ) : (
          <form className="agent-login-form" onSubmit={submitOpenCode}>
            <p className="agent-login-privacy">{tx("ui.opencode_login_description")}</p>
            <label className="field-block">
              <span>{tx("ui.opencode_workspace_id")}</span>
              <input value={opencodeWorkspace} onChange={(event) => setOpenCodeWorkspace(event.target.value)} placeholder={tx("ui.opencode_workspace_placeholder")} autoComplete="off" autoCapitalize="off" spellCheck={false} disabled={submitting} autoFocus />
            </label>
            <label className="field-block">
              <span>{tx("ui.opencode_auth_cookie")}</span>
              <div className="secret-input">
                <input
                  value={opencodeCookie}
                  onChange={(event) => setOpenCodeCookie(event.target.value)}
                  type={opencodeVisible ? "text" : "password"}
                  placeholder={tx("ui.opencode_auth_cookie_placeholder")}
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  disabled={submitting}
                />
                <button type="button" aria-label={tx(opencodeVisible ? "ui.hide_session_json" : "ui.show_session_json")} title={tx(opencodeVisible ? "ui.hide_session_json" : "ui.show_session_json")} onClick={() => setOpenCodeVisible((visible) => !visible)}>
                  {opencodeVisible ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
            </label>
            <p className="agent-login-privacy">{tx("ui.opencode_privacy_notice")}</p>
            {error ? <div className="agent-login-error" role="alert">{error}</div> : null}
            <div className="agent-login-actions">
              <button className="button" type="button" onClick={closeWindow} disabled={submitting}><X size={16} />{tx("ui.cancel")}</button>
              <button className="button button-primary" type="submit" disabled={submitting || opencodeWorkspace.trim() === "" || opencodeCookie.trim() === ""}>
                {submitting ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}
                {tx(submitting ? "ui.opencode_saving" : "ui.opencode_save_and_query")}
              </button>
            </div>
          </form>
        )}

        {completed ? <div className="agent-login-actions"><button className="button button-primary" type="button" onClick={closeWindow}><X size={16} />{tx("ui.close_login_window")}</button></div> : null}
      </section>
    </main>
  );
}

function formatOpenCodeWindow(window: { usage_percent: number; reset_at?: string } | undefined): string {
  if (!window) return "-";
  const percent = `${Math.round(window.usage_percent)}%`;
  if (!window.reset_at) return percent;
  return `${percent} · ${new Date(window.reset_at).toLocaleString()}`;
}
