import { CheckCircle2, ExternalLink, Eye, EyeOff, KeyRound, LoaderCircle, X } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import { readPanelAuth } from "../store/panelAuth";
import { clearSession, setSession } from "../store/session";
import type { AgentIdentitySessionLoginResponse } from "../types";
import { LoginDialog } from "./LoginDialog";

interface AgentIdentitySessionLoginProps {
  loginState: string | null;
}

type AuthenticationState = "booting" | "login" | "ready";

export function AgentIdentitySessionLogin({ loginState }: AgentIdentitySessionLoginProps) {
  const { locale, tx } = useI18n();
  const [authentication, setAuthentication] = useState<AuthenticationState>(loginState ? "booting" : "ready");
  const [authenticationLoading, setAuthenticationLoading] = useState(false);
  const [authenticationError, setAuthenticationError] = useState("");
  const [sessionJSON, setSessionJSON] = useState("");
  const [sessionVisible, setSessionVisible] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<AgentIdentitySessionLoginResponse | null>(null);

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

  return (
    <main className="agent-login-page">
      <section className="agent-login-panel" aria-labelledby="agent-login-title">
        <div className="agent-login-heading">
          <span className={`agent-login-mark ${result ? "is-complete" : ""}`}>{result ? <CheckCircle2 size={22} /> : <KeyRound size={22} />}</span>
          <div>
            <h1 id="agent-login-title">{result ? tx("ui.agent_identity_login_complete") : tx("ui.agent_identity_session_login")}</h1>
            <p>{result ? tx("ui.cpa_is_saving_agent_identity") : tx("ui.agent_identity_session_login_description")}</p>
          </div>
        </div>

        {result ? (
          <div className="agent-login-result" aria-label={tx("ui.agent_identity_login_complete")}>
            <div><span>{tx("ui.accounts")}</span><strong>{result.account.email || tx("ui.unknown")}</strong></div>
            <div><span>{tx("ui.plan_type")}</span><strong>{result.account.plan_type || tx("ui.unknown")}</strong></div>
            <div><span>{tx("ui.provider")}</span><strong>{result.account.provider}</strong></div>
          </div>
        ) : (
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
        )}

        {result ? <div className="agent-login-actions"><button className="button button-primary" type="button" onClick={closeWindow}><X size={16} />{tx("ui.close_login_window")}</button></div> : null}
      </section>
    </main>
  );
}
