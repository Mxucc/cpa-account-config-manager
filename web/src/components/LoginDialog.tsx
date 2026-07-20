import { Eye, EyeOff, KeyRound, LoaderCircle } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useI18n } from "../i18n";

interface LoginDialogProps {
  loading: boolean;
  error: string;
  onSubmit: (baseURL: string, managementKey: string) => Promise<void>;
}

export function LoginDialog({ loading, error, onSubmit }: LoginDialogProps) {
  const { tx } = useI18n();
  const [baseURL, setBaseURL] = useState(window.location.origin);
  const [managementKey, setManagementKey] = useState("");
  const [showKey, setShowKey] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (managementKey.trim() === "") return;
    await onSubmit(baseURL, managementKey);
  };

  return (
    <div className="auth-backdrop">
      <form className="auth-dialog" onSubmit={submit} aria-label={tx("ui.management_authentication")}>
        <div className="auth-mark"><KeyRound size={22} /></div>
        <div>
          <h2>{tx("ui.management_authentication")}</h2>
          <span className="auth-product">CPA Account Config Manager</span>
        </div>
        <label className="field-block">
          <span>{tx("ui.cpa_url")}</span>
          <input value={baseURL} onChange={(event) => setBaseURL(event.target.value)} autoComplete="url" />
        </label>
        <label className="field-block">
          <span>Management Key</span>
          <div className="secret-input">
            <input
              value={managementKey}
              onChange={(event) => setManagementKey(event.target.value)}
              type={showKey ? "text" : "password"}
              autoComplete="current-password"
              autoFocus
            />
            <button type="button" aria-label={tx(showKey ? "ui.hide_key" : "ui.show_key")} title={tx(showKey ? "ui.hide_key" : "ui.show_key")} onClick={() => setShowKey((value) => !value)}>
              {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </label>
        {error ? <div className="auth-error" role="alert">{error}</div> : null}
        <button className="button button-primary auth-submit" type="submit" disabled={loading || managementKey.trim() === ""}>
          {loading ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}
          {tx("ui.verify_and_continue")}
        </button>
      </form>
    </div>
  );
}
