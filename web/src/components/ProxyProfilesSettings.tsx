import { LoaderCircle, Network, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import * as api from "../api/client";
import { operatorMessage } from "../format/operatorMessage";
import { useI18n } from "../i18n";
import type { ProxyProfileView } from "../types";

interface Props {
	refreshRevision?: number;
	onAPIError: (error: unknown) => void;
	onNotice: (message: string) => void;
}

const emptyForm = { id: "", name: "", proxy_url: "", note: "", providers: "", enabled: true };

export function ProxyProfilesSettings({ refreshRevision = 0, onAPIError, onNotice }: Props) {
	const { locale, tx, formatDateTime } = useI18n();
	const [profiles, setProfiles] = useState<ProxyProfileView[]>([]);
	const [storageError, setStorageError] = useState("");
	const [loading, setLoading] = useState(true);
	const [saving, setSaving] = useState(false);
	const [deletingID, setDeletingID] = useState("");
	const [form, setForm] = useState(emptyForm);
	const [error, setError] = useState("");

	const load = useCallback(async (signal?: AbortSignal) => {
		setLoading(true);
		try {
			const response = await api.listProxyProfiles(signal);
			if (signal?.aborted) return;
			setProfiles(response.profiles);
			setStorageError(response.storage_error ?? "");
			setLoading(false);
		} catch (caught) {
			if (signal?.aborted) return;
			setLoading(false);
			if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
			else setError(operatorMessage(caught instanceof Error ? caught.message : "ui.request_failed", locale));
		}
	}, [locale, onAPIError]);

	useEffect(() => {
		const controller = new AbortController();
		void load(controller.signal);
		return () => controller.abort();
	}, [load, refreshRevision]);

	const submit = async () => {
		setSaving(true);
		setError("");
		try {
			const input = {
				name: form.name,
				proxy_url: form.proxy_url,
				note: form.note,
				providers: form.providers.split(/[,;\n]/).map((item) => item.trim()).filter(Boolean),
				enabled: form.enabled,
			};
			if (form.id) await api.updateProxyProfile({ ...input, id: form.id });
			else await api.createProxyProfile(input);
			setForm(emptyForm);
			onNotice(tx("ui.proxy_profile_saved"));
			await load();
		} catch (caught) {
			if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
			else setError(operatorMessage(caught instanceof Error ? caught.message : "ui.request_failed", locale));
		} finally {
			setSaving(false);
		}
	};

	const remove = async (profile: ProxyProfileView) => {
		let force = false;
		if (profile.account_count > 0 && !window.confirm(tx("ui.proxy_profile_delete_force_confirm", { name: profile.name }))) return;
		else if (!profile.account_count && !window.confirm(tx("ui.proxy_profile_delete_confirm", { name: profile.name }))) return;
		force = profile.account_count > 0;
		setDeletingID(profile.id);
		try {
			await api.deleteProxyProfile(profile.id, force);
			if (form.id === profile.id) setForm(emptyForm);
			onNotice(tx("ui.proxy_profile_deleted"));
			await load();
		} catch (caught) {
			if (caught instanceof api.APIError && caught.status === 401) onAPIError(caught);
			else setError(operatorMessage(caught instanceof Error ? caught.message : "ui.request_failed", locale));
		} finally {
			setDeletingID("");
		}
	};

	return (
		<section className="proxy-profiles-settings settings-section" aria-label={tx("ui.proxy_profiles")}>
			<div className="settings-section-heading"><div><strong>{tx("ui.proxy_profiles")}</strong><span>{tx("ui.proxy_profiles_description")}</span></div>
				<button className="button button-quiet" type="button" disabled={loading} onClick={() => void load()}><RefreshCw className={loading ? "spin" : ""} size={15} />{tx("ui.refresh")}</button>
			</div>
			{storageError ? <div className="experimental-storage-error" role="alert">{tx("ui.proxy_profiles_storage_error")}</div> : null}
			<div className="proxy-profile-explainer">{tx("ui.proxy_profiles_usage_help")}</div>
			{error ? <div className="automation-error" role="alert"><span>{error}</span><button type="button" onClick={() => setError("")}>{tx("ui.close")}</button></div> : null}
			<div className="settings-inline-grid proxy-profile-grid">
				<form onSubmit={(event) => { event.preventDefault(); void submit(); }}>
					<label className="filter-control"><span>{tx("ui.profile_name")}</span><input required maxLength={128} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></label>
					<label className="filter-control"><span>{tx("ui.proxy_url_value")}</span><input required={!form.id} placeholder={form.id ? tx("ui.proxy_profile_keep_existing") : "socks5h://gateway.internal:1080"} value={form.proxy_url} onChange={(event) => setForm({ ...form, proxy_url: event.target.value })} autoComplete="off" /></label>
					<label className="filter-control"><span>{tx("ui.provider_scope")}</span><input placeholder={tx("ui.provider_scope_placeholder")} value={form.providers} onChange={(event) => setForm({ ...form, providers: event.target.value })} /></label>
					<label className="filter-control"><span>{tx("ui.note")}</span><textarea rows={2} maxLength={2000} value={form.note} onChange={(event) => setForm({ ...form, note: event.target.value })} /></label>
					<label className="switch-control"><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} /><b>{tx(form.enabled ? "ui.enabled" : "ui.disabled")}</b></label>
					<div className="settings-section-actions">
						<button className="button button-primary" type="submit" disabled={saving}>{saving ? <LoaderCircle className="spin" size={15} /> : form.id ? <Save size={15} /> : <Plus size={15} />}{tx(form.id ? "ui.save_settings" : "ui.add_proxy_profile")}</button>
						{form.id ? <button className="button button-quiet" type="button" onClick={() => setForm(emptyForm)}>{tx("ui.cancel")}</button> : null}
					</div>
				</form>

				<div className="proxy-profile-list" role="list">
					{loading && !profiles.length ? <div className="model-catalog-state"><LoaderCircle className="spin" size={16} />{tx("ui.loading")}</div> : null}
					{!loading && profiles.length === 0 ? <div className="model-catalog-state">{tx("ui.no_proxy_profiles")}</div> : null}
					{profiles.map((profile) => (
						<article role="listitem" key={profile.id} className={!profile.enabled ? "is-disabled" : ""}>
							<header>
								<strong>{profile.name}</strong>
								<span>{ (profile.providers ?? []).length ? (profile.providers ?? []).join(", ") : tx("ui.all_providers")}</span>
							</header>
							<code title={tx("ui.proxy_credentials_hidden")}>{profile.proxy_url_masked}</code>
							<footer>
								<span>{tx("ui.assigned_accounts", { count: profile.account_count })}</span>
								<time>{formatDateTime(profile.updated_at)}</time>
							</footer>
							{profile.note ? <p>{profile.note}</p> : null}
							<div className="proxy-profile-actions">
								<button className="button button-quiet" type="button" onClick={() => setForm({ id: profile.id, name: profile.name, proxy_url: "", note: profile.note ?? "", providers: (profile.providers ?? []).join(", "), enabled: profile.enabled })}>{tx("ui.edit")}</button>
								<button className="button button-danger" type="button" disabled={deletingID === profile.id} onClick={() => void remove(profile)}><Trash2 size={14} />{tx("ui.delete")}</button>
							</div>
						</article>
					))}
				</div>
			</div>
			<p className="field-help">{tx("ui.proxy_profile_edit_secret_notice")}</p>
		</section>
	);
}
