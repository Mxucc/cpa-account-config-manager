import { LockKeyhole, Pencil, Settings2 } from "lucide-react";
import type { Account, UsageWindowSnapshot } from "../types";
import { formatAccountConcurrency } from "../accountConcurrency";
import { accountStateLabel, sourceLabel } from "../format/accountDisplay";
import { operatorMessage } from "../format/operatorMessage";
import { accountAutomationPresentation } from "../format/accountAutomation";
import { localeFormats, useI18n, type Locale } from "../i18n";
import { formatCreditUSD } from "../format/currency";
import { Modal } from "./Modal";

interface AccountDetailsDialogProps {
  account: Account;
  creditUsageEnabled?: boolean;
  weeklyOverdraftEnabled?: boolean;
  onClose: () => void;
  onEdit: () => void;
}

export function AccountDetailsDialog({ account, creditUsageEnabled = false, weeklyOverdraftEnabled = false, onClose, onEdit }: AccountDetailsDialogProps) {
  const { locale, formatDateTime, tx } = useI18n();
  const usage = account.usage;
  const identity = account.label || account.email || account.name || account.id;
  const automation = accountAutomationPresentation(account, locale);
	const modelPolicy = account.model_policy;
	const modelPolicyMode = modelPolicy
		? tx(modelPolicy.mode === "all" ? "ui.all_models" : modelPolicy.mode === "allow_only" ? "ui.model_allowlist" : "ui.model_blocklist")
		: tx("ui.not_managed_by_plugin");

  return (
    <Modal
      title={tx("ui.account_details")}
      wide
      onClose={onClose}
      footer={(
        <>
          <span className="modal-scope">{account.name || account.id}</span>
          <button className="button" type="button" onClick={onClose}>{tx("ui.close")}</button>
          {account.editable ? <button className="button button-primary" type="button" onClick={onEdit}><Pencil size={15} />{tx("ui.edit_account")}</button> : null}
        </>
      )}
    >
      <div className="account-details">
        <div className="account-details-heading">
          <div>
            <strong>{identity}</strong>
            <span>{account.email && account.email !== identity ? account.email : account.name}</span>
          </div>
          {account.editable
            ? <span className="access-tag editable"><Settings2 size={13} />{tx("ui.editable")}</span>
            : <span className="access-tag readonly" title={operatorMessage(account.read_only_reason, locale)}><LockKeyhole size={13} />{tx("ui.read_only")}</span>}
        </div>

        <DetailSection title={tx("ui.identity_and_source")}>
          <DetailItem label={tx("ui.filename")} value={account.name} mono />
          <DetailItem label={tx("ui.account_index")} value={account.id} mono />
          <DetailItem label="Auth ID" value={account.auth_id} mono />
          {account.credential?.account_id ? <DetailItem label={tx("ui.credential_account_id")} value={account.credential.account_id} mono /> : null}

          <DetailItem label={tx("ui.provider")} value={account.provider} />
          <DetailItem label={tx("ui.type")} value={account.type} />
          <DetailItem label={tx("ui.account_type")} value={account.account_type === "agent_identity" ? tx("ui.agent_identity") : account.account_type === "personal_access_token" ? tx("ui.codex_personal_access_token") : account.account_type} />
          <DetailItem label={tx("ui.plan_type")} value={account.plan_type} />
          <DetailItem label={tx("ui.source")} value={sourceLabel(account.source, locale)} />
          <DetailItem label={tx("ui.status")} value={accountStateLabel(account, locale)} />
          {account.credential ? (
            <DetailItem label={tx("ui.credential_runtime")} value={account.credential.runtime_loaded ? tx("ui.runtime_loaded") : account.credential.runtime_error || tx("ui.list_metadata_fallback")} wide />
          ) : null}
          <DetailItem label={tx("ui.status_detail")} value={operatorMessage(account.status_message, locale)} />
          {!account.editable ? <DetailItem label={tx("ui.read_only_reason")} value={operatorMessage(account.read_only_reason, locale)} wide /> : null}
        </DetailSection>

        {account.automation ? (
          <DetailSection title={tx("ui.automated_disposition")}>
            <DetailItem label={tx("ui.disposition")} value={automation?.badge || tx("ui.no_automated_disposition")} />
            <DetailItem label={tx("ui.inspection_conclusion")} value={automation?.reason || tx("ui.no_conclusion")} />
            <DetailItem label={tx("ui.disposition_detail")} value={automation?.detail || tx("ui.no_automated_action_required")} wide />
            <DetailItem label={tx("ui.auto_disable")} value={tx(account.automation.auto_disable_enabled ? "ui.on" : "ui.off")} />
            <DetailItem label={tx("ui.auto_enable")} value={tx(account.automation.auto_enable_enabled ? "ui.on" : "ui.off")} />
            <DetailItem label={tx("ui.auto_delete")} value={tx(account.automation.auto_delete_enabled ? "ui.on" : "ui.off")} />
            <DetailItem label={tx("ui.auto_disabled_at")} value={formatDateTime(account.automation.disabled_at)} />
            <DetailItem label={tx("ui.expected_recovery")} value={formatDateTime(account.automation.recover_after)} />
            <DetailItem label={tx("ui.delete_eligible_at")} value={formatDateTime(account.automation.delete_eligible_at)} />
            <DetailItem label={tx("ui.last_inspected")} value={formatDateTime(account.automation.last_checked_at)} />
          </DetailSection>
        ) : null}

        <DetailSection title={tx("ui.routing")}>
          <DetailItem label={tx("ui.priority")} value={account.priority} mono />
          <DetailItem label={tx("ui.route_prefix")} value={account.prefix || tx("ui.default")} mono />
          <DetailItem label={tx("ui.proxy")} value={account.proxy || tx(account.proxy_configured ? "ui.configured_address_hidden" : "ui.not_configured")} mono />
          <DetailItem label="WebSocket" value={tx(account.websockets === undefined ? "ui.not_set" : account.websockets ? "ui.on_2" : "ui.off_2")} />
          <DetailItem label={tx("ui.headers")} value={account.header_count || 0} mono />
          <DetailItem label={tx("ui.note")} value={account.note} wide />
          {account.header_names?.length ? (
            <div className="detail-item detail-item-wide">
              <span>{tx("ui.header_names")}</span>
              <div className="detail-chips">{account.header_names.map((name) => <code key={name}>{name}</code>)}</div>
            </div>
          ) : null}
        </DetailSection>

		<DetailSection title={tx("ui.plugin_configuration")}>
			<DetailItem label={tx("ui.plugin_configuration_state")} value={tx(modelPolicy ? "ui.managed_by_plugin" : "ui.not_managed_by_plugin")} />
			<DetailItem label={tx("ui.account_concurrency")} value={!account.concurrency?.supported ? tx("ui.unavailable") : formatAccountConcurrency(account.concurrency)} mono />
			<DetailItem label={tx("ui.model_policy_mode")} value={modelPolicyMode} />
			<DetailItem label={tx("ui.managed_model_exclusions")} value={modelPolicy?.excluded_count ?? 0} mono />
			<div className="detail-item detail-item-wide">
				<span>{tx("ui.managed_models")}</span>
				{modelPolicy?.models?.length ? <div className="detail-chips">{modelPolicy.models.map((model) => <code key={model}>{model}</code>)}</div> : <strong>-</strong>}
			</div>
		</DetailSection>

        <DetailSection title={tx("ui.usage_and_activity")}>
          <DetailItem label={tx("ui.successful_requests")} value={formatNumber(account.success, locale)} mono />
          <DetailItem label={tx("ui.failed_requests")} value={formatNumber(account.failed, locale)} mono />
          <DetailItem label={tx("ui.total_tokens")} value={usage ? formatNumber(usage.total_tokens, locale) : tx("ui.no_data")} mono />
          {creditUsageEnabled ? <DetailItem label={tx("ui.estimated_credit_usage")} value={usage?.credit ? formatCreditUSD(usage.credit.amount_usd, locale) : tx("ui.awaiting_credit_usage_collection")} mono /> : null}
          {creditUsageEnabled ? <DetailItem label={tx("ui.rated_requests")} value={usage?.credit ? formatNumber(usage.credit.rated_requests, locale) : tx("ui.no_data")} mono /> : null}
          {creditUsageEnabled ? <DetailItem label={tx("ui.unrated_requests")} value={usage?.credit ? formatNumber(usage.credit.unrated_requests, locale) : tx("ui.no_data")} mono /> : null}
          {creditUsageEnabled ? <DetailItem label={tx("ui.credit_usage_started_at")} value={formatDateTime(usage?.credit?.started_at)} /> : null}
          {creditUsageEnabled ? <DetailItem label={tx("ui.pricing_updated_at")} value={formatDateTime(usage?.credit?.pricing_updated_at)} /> : null}
          {creditUsageEnabled ? <DetailItem label={tx("ui.credit_pricing_source")} value={usage?.credit?.pricing_source || tx("ui.no_data")} wide /> : null}
          {creditUsageEnabled && weeklyOverdraftEnabled && usage?.codex?.five_hour?.overdraft_active ? <DetailItem label={tx("ui.5_hour_overdraft_credit_usage")} value={formatOverdraftCredit(usage.codex.five_hour, locale, tx)} wide /> : null}
          {creditUsageEnabled && weeklyOverdraftEnabled && usage?.codex?.seven_day?.overdraft_active ? <DetailItem label={tx("ui.7_day_overdraft_credit_usage")} value={formatOverdraftCredit(usage.codex.seven_day, locale, tx)} wide /> : null}
          <DetailItem label="Input" value={usage ? formatNumber(usage.input_tokens, locale) : tx("ui.no_data")} mono />
          <DetailItem label="Output" value={usage ? formatNumber(usage.output_tokens, locale) : tx("ui.no_data")} mono />
          <DetailItem label="Reasoning" value={usage ? formatNumber(usage.reasoning_tokens, locale) : tx("ui.no_data")} mono />
          <DetailItem label="Cached" value={usage ? formatNumber(usage.cached_tokens + usage.cache_read_tokens, locale) : tx("ui.no_data")} mono />
          <DetailItem label={tx("ui.last_request")} value={formatDateTime(usage?.last_request_at)} />
          <DetailItem label={tx("ui.5_hour_usage")} value={usage?.codex?.five_hour ? `${formatPercent(usage.codex.five_hour.used_percent)} · ${formatDateTime(usage.codex.five_hour.reset_at)}` : tx("ui.no_data")} />
          <DetailItem label={tx("ui.7_day_usage")} value={usage?.codex?.seven_day ? `${formatPercent(usage.codex.seven_day.used_percent)} · ${formatDateTime(usage.codex.seven_day.reset_at)}` : tx("ui.no_data")} />
						<DetailItem label={tx("ui.active_reset_count")} value={usage?.codex?.active_reset_count !== undefined ? formatNumber(usage.codex.active_reset_count, locale) : tx("ui.no_data")} mono />
        </DetailSection>

        <DetailSection title={tx("ui.time")}>
          <DetailItem label={tx("ui.updated")} value={formatDateTime(account.updated_at)} />
          <DetailItem label={tx("ui.last_refresh")} value={formatDateTime(account.last_refresh)} />
          <DetailItem label={tx("ui.next_retry")} value={formatDateTime(account.next_retry_after)} />
        </DetailSection>
      </div>
    </Modal>
  );
}

function DetailSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="detail-section">
      <h3>{title}</h3>
      <div className="detail-grid">{children}</div>
    </section>
  );
}

function DetailItem({ label, value, mono = false, wide = false }: { label: string; value: string | number | undefined; mono?: boolean; wide?: boolean }) {
  const shown = value === undefined || value === "" ? "-" : String(value);
  return (
    <div className={`detail-item ${wide ? "detail-item-wide" : ""}`}>
      <span>{label}</span>
      {mono ? <code title={shown}>{shown}</code> : <strong title={shown}>{shown}</strong>}
    </div>
  );
}

function formatOverdraftCredit(window: UsageWindowSnapshot, locale: Locale, tx: ReturnType<typeof useI18n>["tx"]): string {
  return tx("ui.overdraft_credit_usage_detail", {
    amount: formatCreditUSD(window.overdraft_amount_usd ?? 0, locale),
    rated: formatNumber(window.overdraft_rated_requests ?? 0, locale),
    unrated: formatNumber(window.overdraft_unrated_requests ?? 0, locale),
  });
}

function formatNumber(value: number, locale: Locale): string {
  return new Intl.NumberFormat(localeFormats[locale].dateTimeLocale, { notation: value >= 100000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value);
}

function formatPercent(value: number): string {
  const normalized = value <= 1 ? value * 100 : value;
  return `${Math.max(0, Math.min(100, normalized)).toFixed(1).replace(/\.0$/, "")}%`;
}
