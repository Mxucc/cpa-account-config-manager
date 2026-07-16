import { LoaderCircle, Trash2, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import type { Account, AccountDeletePreview } from "../types";
import { Modal } from "./Modal";

interface DeleteAccountDialogProps {
  account: Account;
  preview: AccountDeletePreview | null;
  previewing: boolean;
  deleting: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => void;
}

export function DeleteAccountDialog({ account, preview, previewing, deleting, error, onClose, onConfirm }: DeleteAccountDialogProps) {
  const [confirmation, setConfirmation] = useState("");
  const filename = preview?.account.name || account.name;
  const confirmed = Boolean(preview && confirmation === preview.account.name);

  useEffect(() => {
    setConfirmation("");
  }, [preview?.id, account.id]);

  return (
    <Modal
      title="删除账号"
      onClose={deleting ? () => undefined : onClose}
      footer={(
        <>
          <button className="button" type="button" disabled={deleting} onClick={onClose}>取消</button>
          <button className="button button-danger" type="button" disabled={!confirmed || deleting} onClick={onConfirm}>
            {deleting ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}删除账号
          </button>
        </>
      )}
    >
      <div className="delete-account-dialog">
        <div className="delete-warning">
          <TriangleAlert size={20} />
          <div>
            <strong>将永久删除 CPA Auth 文件</strong>
            <span>删除前会再次检查文件是否自预览后发生变化。</span>
          </div>
        </div>

        <dl className="delete-account-summary">
          <div><dt>账号</dt><dd>{preview?.account.label || preview?.account.email || account.label || account.email || account.id}</dd></div>
          <div><dt>文件名</dt><dd><code>{filename}</code></dd></div>
          <div><dt>Provider</dt><dd>{preview?.account.provider || account.provider || account.type || "unknown"}</dd></div>
        </dl>

        {previewing ? <div className="delete-preview-loading"><LoaderCircle className="spin" size={18} /><span>正在校验删除目标</span></div> : null}
        {error ? <div className="form-error" role="alert">{error}</div> : null}
        {preview ? (
          <label className="delete-confirmation">
            <span>输入文件名确认</span>
            <code>{preview.account.name}</code>
            <input
              autoComplete="off"
              spellCheck={false}
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              aria-label="确认删除文件名"
              placeholder={preview.account.name}
              disabled={deleting}
            />
          </label>
        ) : null}
      </div>
    </Modal>
  );
}
