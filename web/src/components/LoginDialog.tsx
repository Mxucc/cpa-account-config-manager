import { Eye, EyeOff, KeyRound, LoaderCircle } from "lucide-react";
import { useState, type FormEvent } from "react";

interface LoginDialogProps {
  loading: boolean;
  error: string;
  onSubmit: (baseURL: string, managementKey: string) => Promise<void>;
}

export function LoginDialog({ loading, error, onSubmit }: LoginDialogProps) {
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
      <form className="auth-dialog" onSubmit={submit} aria-label="管理认证">
        <div className="auth-mark"><KeyRound size={22} /></div>
        <div>
          <h2>管理认证</h2>
          <span className="auth-product">CPA Account Config Manager</span>
        </div>
        <label className="field-block">
          <span>CPA 地址</span>
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
            <button type="button" aria-label={showKey ? "隐藏 Key" : "显示 Key"} title={showKey ? "隐藏 Key" : "显示 Key"} onClick={() => setShowKey((value) => !value)}>
              {showKey ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
        </label>
        {error ? <div className="auth-error" role="alert">{error}</div> : null}
        <button className="button button-primary auth-submit" type="submit" disabled={loading || managementKey.trim() === ""}>
          {loading ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}
          验证并进入
        </button>
      </form>
    </div>
  );
}
