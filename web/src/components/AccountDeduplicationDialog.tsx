import { AlertCircle, CheckCircle2, FileJson2, LoaderCircle, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { UIMessageKey } from "../i18n/uiText";
import type { AccountDeduplicationGroup, AccountDeduplicationOptions, AccountDeduplicationPreview } from "../types";
import { Modal } from "./Modal";

interface AccountDeduplicationDialogProps {
  preview: AccountDeduplicationPreview;
  loading: boolean;
  reviewing: boolean;
  error?: string;
  onClose: () => void;
  onOptionsChange: (options: AccountDeduplicationOptions) => void;
  onReview: (accountIDs: string[]) => void;
}

const keepReasonLabels: Record<AccountDeduplicationGroup["keep_reason"], UIMessageKey> = {
  editable_physical_file: "ui.dedup_keep_reason_editable",
  enabled_account: "ui.dedup_keep_reason_enabled",
  healthier_account: "ui.dedup_keep_reason_healthier",
  newer_evidence: "ui.dedup_keep_reason_newer",
  more_complete_credential: "ui.dedup_keep_reason_complete",
  deterministic_order: "ui.dedup_keep_reason_deterministic",
};

const matchLabels: Record<AccountDeduplicationGroup["matched_by"], UIMessageKey> = {
  account_id: "ui.duplicate_match_account_id",
  email: "ui.duplicate_match_email",
  multiple: "ui.duplicate_match_multiple",
};

export function AccountDeduplicationDialog({ preview, loading, reviewing, error = "", onClose, onOptionsChange, onReview }: AccountDeduplicationDialogProps) {
  const { locale, tx } = useI18n();
  const [keepByGroup, setKeepByGroup] = useState<Record<string, string>>(() => Object.fromEntries(preview.groups.map((group) => [group.id, group.keep_id])));
  const [selected, setSelected] = useState<Set<string>>(() => new Set(preview.groups.flatMap((group) => group.members.filter((member) => member.recommended_action === "delete").map((member) => member.id))));
  const selectedIDs = useMemo(() => Array.from(selected).sort(), [selected]);

  useEffect(() => {
    setKeepByGroup(Object.fromEntries(preview.groups.map((group) => [group.id, group.keep_id])));
    setSelected(new Set(preview.groups.flatMap((group) => group.members.filter((member) => member.recommended_action === "delete").map((member) => member.id))));
  }, [preview]);

  const chooseKeep = (group: AccountDeduplicationGroup, nextKeepID: string) => {
    const previousKeepID = keepByGroup[group.id] ?? group.keep_id;
    setKeepByGroup((current) => ({ ...current, [group.id]: nextKeepID }));
    setSelected((current) => {
      const next = new Set(current);
      const previous = group.members.find((member) => member.id === previousKeepID);
      if (previous?.editable && previous.id !== nextKeepID) next.add(previous.id);
      next.delete(nextKeepID);
      return next;
    });
  };

  const toggleDelete = (id: string, checked: boolean) => {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  return (
    <Modal
      title={tx("ui.account_deduplication")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{tx("ui.selected_duplicate_credentials", { count: selectedIDs.length })}</span>
          <button className="button" type="button" onClick={onClose}>{tx("ui.cancel")}</button>
          <button className="button button-danger" type="button" disabled={loading || reviewing || selectedIDs.length === 0} onClick={() => onReview(selectedIDs)}>
            {reviewing ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}
            {tx("ui.review_duplicate_deletions", { count: selectedIDs.length })}
          </button>
        </>
      )}
    >
      <div className="deduplication-intro">
        <ShieldCheck size={18} />
        <div><strong>{tx("ui.account_deduplication_description")}</strong><span>{tx("ui.account_deduplication_safety_note")}</span></div>
      </div>
      <fieldset className="deduplication-options" disabled={loading} aria-busy={loading}>
        <legend>{tx("ui.deduplication_rules")}</legend>
        <label>
          <input type="checkbox" checked={preview.options.ignore_account_id} onChange={(event) => onOptionsChange({ ...preview.options, ignore_account_id: event.target.checked })} />
          <span><strong>{tx("ui.ignore_account_ids")}</strong><small>{tx("ui.ignore_account_ids_description")}</small></span>
        </label>
        <label>
          <input type="checkbox" checked={preview.options.exclude_team_accounts} onChange={(event) => onOptionsChange({ ...preview.options, exclude_team_accounts: event.target.checked })} />
          <span><strong>{tx("ui.exclude_team_accounts")}</strong><small>{tx("ui.exclude_team_accounts_description")}</small></span>
        </label>
        {loading ? <span className="deduplication-refresh" role="status"><LoaderCircle className="spin" size={14} />{tx("ui.refreshing_deduplication")}</span> : null}
      </fieldset>
      <div className="deduplication-metrics">
        <DeduplicationMetric label={tx("ui.scanned_credentials")} value={preview.scanned_credentials} />
        <DeduplicationMetric label={tx("ui.excluded_credentials")} value={preview.excluded_credentials} />
        <DeduplicationMetric label={tx("ui.duplicate_groups")} value={preview.duplicate_groups} tone={preview.duplicate_groups > 0 ? "warning" : "success"} />
        <DeduplicationMetric label={tx("ui.duplicate_credentials")} value={preview.duplicate_credentials} tone={preview.duplicate_credentials > 0 ? "warning" : ""} />
        <DeduplicationMetric label={tx("ui.proposed_deletions")} value={preview.proposed_deletions} tone={preview.proposed_deletions > 0 ? "danger" : ""} />
        <DeduplicationMetric label={tx("ui.read_only_skipped")} value={preview.read_only_skipped} />
        <DeduplicationMetric label={tx("ui.missing_identity")} value={preview.missing_identity} />
      </div>
      {error ? <div className="preview-start-error" role="alert"><AlertCircle size={18} /><div><strong>{tx("ui.unable_to_prepare_deletion_preview")}</strong><span>{error}</span></div></div> : null}
      {preview.groups.length === 0 ? (
        <div className="deduplication-empty" role="status"><CheckCircle2 size={24} /><strong>{tx("ui.no_duplicate_accounts")}</strong><span>{tx("ui.no_duplicate_accounts_description")}</span></div>
      ) : (
        <div className="deduplication-groups">
          {preview.groups.map((group) => {
            const keepID = keepByGroup[group.id] ?? group.keep_id;
            return (
              <section className="deduplication-group" key={group.id}>
                <header>
                  <div><strong>{group.identity_label || tx("ui.duplicate_identity")}</strong><span>{tx(matchLabels[group.matched_by])} · {group.provider}</span></div>
                  <span>{tx("ui.count_credentials", { count: group.members.length })}</span>
                </header>
                <div className="deduplication-members">
                  {group.members.map((member, memberIndex) => {
                    const retained = keepID === member.id;
                    const removable = member.editable && !retained;
                    const checked = removable && selected.has(member.id);
                    return (
                      <div className={`deduplication-member ${retained ? "is-retained" : ""}`} key={`${group.id}:${member.id}:${memberIndex}`}>
                        <label className="deduplication-keep-control">
                          <input type="radio" name={`dedup-keep-${group.id}`} checked={retained} disabled={!member.editable} onChange={() => chooseKeep(group, member.id)} />
                          {retained ? <ShieldCheck size={16} /> : <FileJson2 size={16} />}
                          <span><strong>{member.email || member.name || member.id}</strong><small>{member.name || member.id}</small></span>
                        </label>
                        <span className="provider-tag">{member.plan_type || member.type || member.provider || tx("ui.unknown")}</span>
                        <span className={`deduplication-state ${member.disabled || member.unavailable ? "warning" : "success"}`}>{member.disabled ? tx("ui.disabled") : member.unavailable ? tx("ui.temporarily_unavailable") : tx("ui.enabled")}</span>
                        <label className={`deduplication-delete-control ${!removable ? "is-disabled" : ""}`}>
                          <input type="checkbox" checked={checked} disabled={!removable} onChange={(event) => toggleDelete(member.id, event.target.checked)} />
                          <span>{retained ? tx("ui.retain_credential") : member.editable ? tx("ui.delete_duplicate_credential") : operatorMessage(member.read_only_reason, locale) || tx("ui.read_only_skipped")}</span>
                        </label>
                      </div>
                    );
                  })}
                </div>
                <footer><ShieldCheck size={14} /><span>{tx("ui.recommended_retention_reason")}: {tx(keepReasonLabels[group.keep_reason])}</span></footer>
              </section>
            );
          })}
        </div>
      )}
    </Modal>
  );
}

function DeduplicationMetric({ label, value, tone = "" }: { label: string; value: number; tone?: string }) {
  return <div className={tone}><span>{label}</span><strong>{value}</strong></div>;
}
