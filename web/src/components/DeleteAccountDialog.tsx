import { LoaderCircle, Trash2, TriangleAlert } from "lucide-react";
import type { Account, AccountDeletePreview } from "../types";
import { Modal } from "./Modal";
import { useI18n } from "../i18n";

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
  const { tx } = useI18n();
  const filename = preview?.account.name || account.name;

  return (
    <Modal
      title={tx("ui.delete_account")}
      onClose={deleting ? () => undefined : onClose}
      footer={(
        <>
          <button className="button" type="button" disabled={deleting} onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-danger" type="button" disabled={!preview || deleting} onClick={onConfirm}>
            {deleting ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}{tx("ui.delete_account")}
          </button>
        </>
      )}
    >
      <div className="delete-account-dialog">
        <div className="delete-warning">
          <TriangleAlert size={20} />
          <div>
            <strong>{tx("ui.this_permanently_deletes_the_cpa_auth_file")}</strong>
            <span>{tx("ui.the_file_is_revalidated_before_deletion")}</span>
          </div>
        </div>

        <dl className="delete-account-summary">
          <div><dt>{tx("ui.accounts")}</dt><dd>{preview?.account.label || preview?.account.email || account.label || account.email || account.id}</dd></div>
          <div><dt>{tx("ui.filename")}</dt><dd><code>{filename}</code></dd></div>
          <div><dt>{tx("ui.provider")}</dt><dd>{preview?.account.provider || account.provider || account.type || tx("ui.unknown")}</dd></div>
        </dl>

        {previewing ? <div className="delete-preview-loading"><LoaderCircle className="spin" size={18} /><span>{tx("ui.validating_deletion_target")}</span></div> : null}
        {error ? <div className="form-error" role="alert">{error}</div> : null}
      </div>
    </Modal>
  );
}
