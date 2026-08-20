import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { disable as disableAutostart, enable as enableAutostart, isEnabled as isAutostartEnabled } from "@tauri-apps/plugin-autostart";
import { useEffect, useState } from "react";
import { connection, coreApi } from "../../shared/api/client";
import type { AuditSettings, LicenseState, LicenseStatus, PolicySettings } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { queryKeys, useSettingsData } from "../../app/useCore";
import { useI18n } from "../../shared/i18n/I18n";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { PageState } from "../../shared/ui/PageState";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";
import { hasAdvancedLicense } from "../../shared/license";
import { AdvancedBadge } from "../../shared/ui/Status";
import { localeConfig, localeOptions, resolveLocale } from "../../shared/i18n/locales";

const inTauri = "__TAURI_INTERNALS__" in window;

type SettingsData = { settings: AuditSettings; policy: PolicySettings; license: LicenseState };
type DesktopGatewaySettings = { api_gateway: string; http_proxy: string; allow_lan: boolean };

export function SettingsView() {
  const query = useSettingsData();
  if (query.isPending || !query.settings || !query.policy || !query.license) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <SettingsContent data={{ settings: query.settings, policy: query.policy, license: query.license }}/>;
}

function SettingsContent({ data }: { data: SettingsData }) {
  const { t, locale, setLocale } = useI18n();
  const { notify } = useApp();
  const qc = useQueryClient();
  const [audit, setAudit] = useState<AuditSettings>(data.settings);
  const [policy, setPolicy] = useState<PolicySettings>(data.policy);
  const [clear, setClear] = useState(false);
  const [hfBaseURL, setHFBaseURL] = useState(data.settings.hf_base_url || "");
  const [autostart, setAutostart] = useState(false);
  const [autostartReady, setAutostartReady] = useState(false);
  const [apiGatewayPort, setAPIGatewayPort] = useState(connection.proxyPort());
  const [httpProxyPort, setHTTPProxyPort] = useState(connection.forwardProxyPort());
  const [allowLan, setAllowLan] = useState(connection.allowLan());
  const [gatewaySettingsReady, setGatewaySettingsReady] = useState(!inTauri);
  const [license, setLicense] = useState<LicenseState>(data.license);
  const advanced = hasAdvancedLicense(license);

  useEffect(() => {
    if (!inTauri) return;
    isAutostartEnabled().then(setAutostart).catch(() => {}).finally(() => setAutostartReady(true));
  }, []);

  useEffect(() => setLicense(data.license), [data.license]);

  useEffect(() => {
    if (!inTauri) return;
    invoke<DesktopGatewaySettings>("get_gateway_settings")
      .then(saved => {
        const apiPort = Number(saved.api_gateway.split(":").at(-1));
        const httpPort = Number(saved.http_proxy.split(":").at(-1));
        if (Number.isInteger(apiPort) && apiPort > 0) {
          setAPIGatewayPort(apiPort);
          connection.saveGatewayPort(apiPort);
        }
        if (Number.isInteger(httpPort) && httpPort > 0) {
          setHTTPProxyPort(httpPort);
          connection.saveForwardProxyPort(httpPort);
        }
        const licensedAllowLan = saved.allow_lan && hasAdvancedLicense(data.license);
        setAllowLan(licensedAllowLan);
        connection.saveAllowLan(licensedAllowLan);
      })
      .catch(() => {})
      .finally(() => setGatewaySettingsReady(true));
  }, []);

  async function toggleAutostart(next: boolean) {
    setAutostart(next);
    try {
      if (next) await enableAutostart(); else await disableAutostart();
    } catch (error) {
      setAutostart(!next);
      notify(String(error));
    }
  }

  const savePolicy = useMutation({
    mutationFn: (patch: Partial<PolicySettings>) => coreApi.updatePolicy(patch),
    onSuccess: saved => { setPolicy(saved); qc.setQueryData(queryKeys.policy, saved); },
    onError: error => { setPolicy(data.policy); notify(String(error)); },
  });
  const saveAudit = useMutation({
    mutationFn: (next: AuditSettings) => coreApi.saveSettings(next),
    onSuccess: result => { setAudit(result.audit); qc.setQueryData(queryKeys.settings, result.audit); },
    onError: error => { setAudit(data.settings); notify(String(error)); },
  });
  const clearMutation = useMutation({ mutationFn: coreApi.clearLogs, onSuccess: () => { setClear(false); qc.invalidateQueries({ queryKey: ["core", "audit-logs"] }); } });
  const saveGatewaySettings = useMutation({
    mutationFn: async ({ apiPort, httpPort, allowLan: nextAllowLan }: { apiPort: number; httpPort: number; allowLan: boolean }) => {
      const corePort = Number(new URL(connection.core()).port) || 80;
      if (![apiPort, httpPort].every(port => Number.isInteger(port) && port >= 1 && port <= 65535) || apiPort === httpPort || apiPort === corePort || httpPort === corePort) throw new Error(t("proxyGatewayPortInvalid"));
      const previousAPI = connection.proxyPort();
      const previousHTTP = connection.forwardProxyPort();
      const previousAllowLan = connection.allowLan();
      if (apiPort === previousAPI && httpPort === previousHTTP && nextAllowLan === previousAllowLan) return { restarted: false, apiPort, httpPort, allowLan: nextAllowLan };
      if (inTauri) {
        try {
          await invoke("restart_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${apiPort}`, forwardProxyAddress: `127.0.0.1:${httpPort}`, allowLan: nextAllowLan });
        } catch (error) {
          await invoke("restart_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${previousAPI}`, forwardProxyAddress: `127.0.0.1:${previousHTTP}`, allowLan: previousAllowLan });
          throw error;
        }
      }
      return { restarted: inTauri, apiPort, httpPort, allowLan: nextAllowLan };
    },
    onSuccess: ({ restarted, apiPort, httpPort, allowLan: savedAllowLan }) => {
      connection.saveGatewayPort(apiPort);
      connection.saveForwardProxyPort(httpPort);
      connection.saveAllowLan(savedAllowLan);
      window.setTimeout(() => void qc.invalidateQueries({ queryKey: queryKeys.root }), restarted ? 700 : 0);
      notify(t(restarted ? "settingsSavedRestarted" : "settingsSaved"));
    },
    onError: error => {
      setAPIGatewayPort(connection.proxyPort());
      setHTTPProxyPort(connection.forwardProxyPort());
      setAllowLan(connection.allowLan());
      notify(String(error));
    },
  });
  const importLicense = useMutation({
    mutationFn: coreApi.importLicense,
    onSuccess: result => {
      setLicense(result);
      qc.setQueryData(queryKeys.license, result);
      notify(t("licenseImported"));
    },
    onError: error => notify(String(error)),
  });

  function updatePolicy(patch: Partial<PolicySettings>) {
    const next = { ...policy, ...patch };
    setPolicy(next);
    savePolicy.mutate(patch);
  }
  function updateAudit(patch: Partial<AuditSettings>) {
    const next = { ...audit, ...patch };
    setAudit(next);
    saveAudit.mutate(next);
  }
  function applyHFBaseURL() { const next = { ...audit, hf_base_url: hfBaseURL.trim() }; setAudit(next); saveAudit.mutate(next); }
  function applyGatewaySettings(nextAllowLan = allowLan) { if (!saveGatewaySettings.isPending) saveGatewaySettings.mutate({ apiPort: apiGatewayPort, httpPort: httpProxyPort, allowLan: nextAllowLan }); }
  function toggleAllowLan(next: boolean) {
    if (next && !advanced) { notify(t("advancedRequired")); return; }
    setAllowLan(next); applyGatewaySettings(next);
  }
  async function copyDeviceID() {
    await navigator.clipboard.writeText(license.device_id);
    notify(t("deviceIdCopied"));
  }
  async function selectLicense(event: React.ChangeEvent<HTMLInputElement>) {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    if (file.size < 1 || file.size > 64 * 1024) {
      notify(t("licenseFileSizeInvalid"));
      return;
    }
    try {
      importLicense.mutate(await file.text());
    } catch (error) {
      notify(String(error));
    }
  }
  async function openPurchase() {
    if (!license.device_id) return;
    try {
      if (inTauri) {
        await invoke("open_purchase_page", { deviceId: license.device_id });
      } else {
        const url = new URL(import.meta.env.VITE_PURCHASE_URL || "https://remask.app/buy");
        url.searchParams.set("product", "remask-desktop");
        url.searchParams.set("device_id", license.device_id);
        window.open(url, "_blank", "noopener,noreferrer");
      }
    } catch (error) {
      notify(String(error));
    }
  }

  const licenseStatusLabels: Record<LicenseStatus, string> = {
    missing: t("licenseMissing"), valid: t("licenseValid"), expired: t("licenseExpired"),
    not_yet_valid: t("licenseNotYetValid"), device_mismatch: t("licenseDeviceMismatch"),
    invalid: t("licenseInvalid"), key_unconfigured: t("licenseKeyUnconfigured"),
    device_unavailable: t("licenseDeviceUnavailable"),
  };
  const licenseDate = license.expires_at ? new Intl.DateTimeFormat(localeConfig[locale].dateLocale, { dateStyle: "medium" }).format(new Date(license.expires_at)) : t("notAvailable");
  const licenseSection = <SettingsSection tone="license" title={t("licenseSettings")} subtitle={t("licenseSettingsSub")}>
    <SettingRow label={t("licenseStatus")} description={license.status === "valid" && license.edition ? `${licenseStatusLabels[license.status]} · ${license.edition}` : licenseStatusLabels[license.status]}><span className={`license-status license-status--${license.status}`}>{licenseStatusLabels[license.status]}</span></SettingRow>
    <SettingRow label={t("deviceId")} description={t("deviceIdSub")}><button className="license-device" type="button" disabled={!license.device_id} onClick={() => void copyDeviceID()}><code>{license.device_id || t("notAvailable")}</code><small>{t("copy")}</small></button></SettingRow>
    {license.email ? <SettingRow label={t("licenseEmail")} description={t("licenseEmailSub")}><span className="license-email">{license.email}</span></SettingRow> : null}
    <SettingRow last label={t("licenseExpiresAt")} description={t("licenseExpiresAtSub")}><time className="license-expiry" dateTime={license.expires_at}>{licenseDate}</time></SettingRow>
    <div className="settings-actions license-actions">
      <label className={`button button--secondary license-action-button ${importLicense.isPending ? "license-import--disabled" : ""}`}><input type="file" accept=".license,application/json" disabled={importLicense.isPending} onChange={event => void selectLicense(event)}/>{importLicense.isPending ? t("licenseImporting") : t("importLicense")}</label>
      <Button className="license-action-button" variant="secondary" disabled={!license.device_id} onClick={() => void openPurchase()}>{t("buyLicense")}</Button>
    </div>
  </SettingsSection>;

  return <div className="settings-page"><div className="settings-grid">
    <SettingsSection tone="interface" title={t("applicationSettings")} subtitle={t("applicationSettingsSub")}>
      <SettingRow label={t("language")} description={localeConfig[locale].displayName}><Select className="settings-control" value={locale} onValueChange={value => { const next = resolveLocale(value); if (next) setLocale(next); }} options={localeOptions}/></SettingRow>
      {inTauri && <SettingRow last label={t("autostart")} description={t("autostartSub")}><Switch ariaLabel={t("autostart")} disabled={!autostartReady} checked={autostart} onCheckedChange={toggleAutostart}/></SettingRow>}
    </SettingsSection>
    <SettingsSection tone="gateway" title={t("gatewaySettings")} subtitle={t("gatewaySettingsSub")}>
      <SettingRow label={t("apiGatewayPort")} description={t("apiGatewayPortSub")}><Input className="settings-control" type="number" min={1} max={65535} disabled={!gatewaySettingsReady || saveGatewaySettings.isPending} value={apiGatewayPort} onChange={event => setAPIGatewayPort(Number(event.target.value))} onBlur={() => applyGatewaySettings()} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow label={t("proxyGatewayPort")} description={t("proxyGatewayPortSub")}><Input className="settings-control" type="number" min={1} max={65535} disabled={!gatewaySettingsReady || saveGatewaySettings.isPending} value={httpProxyPort} onChange={event => setHTTPProxyPort(Number(event.target.value))} onBlur={() => applyGatewaySettings()} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow last label={<LabelWithBadge label={t("allowLanRequests")} badge={t("advancedPlanBadge")}/>} description={t("allowLanRequestsSub")}><Switch ariaLabel={t("allowLanRequests")} disabled={!advanced || !gatewaySettingsReady || saveGatewaySettings.isPending} checked={advanced && allowLan} onCheckedChange={toggleAllowLan}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="models" title={t("modelSettings")} subtitle={t("modelSettingsSub")}>
      <SettingRow label={t("hfMirror")} description={t("hfMirrorSub")}><Input className="settings-control" placeholder="https://huggingface.co" value={hfBaseURL} onChange={event => setHFBaseURL(event.target.value)} onBlur={applyHFBaseURL} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow label={t("maxInferenceTokens")} description={t("maxInferenceTokensSub")}><Select className="settings-control" value={String(audit.max_inference_tokens || 512)} onValueChange={value => updateAudit({ max_inference_tokens: Number(value) })} options={[512, 1024, 2048, 4096].map(value => ({ value: String(value), label: `${value} ${t("tokens")}` }))}/></SettingRow>
      <SettingRow last label={t("inferenceProvider")} description={t("inferenceProviderSub")}><Select className="settings-control" value={audit.inference_provider || "cpu"} onValueChange={value => updateAudit({ inference_provider: value as AuditSettings["inference_provider"] })} options={[{ value: "auto", label: t("providerAuto") }, { value: "cpu", label: t("providerCPU") }, { value: "gpu", label: t("providerGPU") }]}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="protection" title={t("redactionSettings")} subtitle={t("redactionSettingsSub")}>
      <SettingRow label={t("redactSystemMessages")} description={t("redactSystemMessagesSub")}><Switch ariaLabel={t("redactSystemMessages")} disabled={savePolicy.isPending} checked={policy.redact_system_messages} onCheckedChange={redact_system_messages => updatePolicy({ redact_system_messages })}/></SettingRow>
      <SettingRow label={t("redactAIAnswers")} description={t("redactAIAnswersSub")}><Switch ariaLabel={t("redactAIAnswers")} disabled={savePolicy.isPending} checked={policy.redact_ai_answers} onCheckedChange={redact_ai_answers => updatePolicy({ redact_ai_answers })}/></SettingRow>
      <SettingRow label={t("entityCache")} description={t("entityCacheSub")}><Switch ariaLabel={t("entityCache")} disabled={saveAudit.isPending} checked={audit.entity_cache_enabled !== false} onCheckedChange={entity_cache_enabled => updateAudit({ entity_cache_enabled })}/></SettingRow>
      <SettingRow last label={t("entityCacheTTL")} description={t("entityCacheTTLSub")}><Select className="settings-control" value={String(audit.entity_cache_ttl_seconds || 900)} onValueChange={value => updateAudit({ entity_cache_ttl_seconds: Number(value) })} options={[60, 300, 900, 3600].map(value => ({ value: String(value), label: `${value < 3600 ? value / 60 + " min" : "1 h"}` }))}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="logs" title={t("audit")} subtitle={t("auditSub")}>
      <SettingRow label={t("recordRedactedContent")} description={t("recordRedactedContentSub")}><Switch ariaLabel={t("recordRedactedContent")} disabled={saveAudit.isPending} checked={audit.record_request_content} onCheckedChange={record_request_content => updateAudit({ record_request_content })}/></SettingRow>
	  <SettingRow label={<LabelWithBadge label={t("recordRawRequest")} badge={t("advancedPlanBadge")}/>} description={t("recordRawRequestSub")}><Switch ariaLabel={t("recordRawRequest")} disabled={!advanced || saveAudit.isPending} checked={advanced && audit.record_raw_request} onCheckedChange={record_raw_request => updateAudit({ record_raw_request })}/></SettingRow>
      <SettingRow last label={t("retention")} description={t("retentionSub")}><Select className="settings-control" value={String(audit.retention_days)} onValueChange={value => updateAudit({ retention_days: Number(value) })} options={[7, 30, 90, 180].map(value => ({ value: String(value), label: `${value} ${t("days")}` }))}/></SettingRow>
      <div className="settings-actions settings-actions--split"><Button variant="danger" onClick={() => setClear(true)}>{t("clearLogs")}</Button></div>
    </SettingsSection>
    {licenseSection}
    <Dialog open={clear} title={t("confirmClear")} onClose={() => setClear(false)} footer={<><Button onClick={() => setClear(false)}>{t("cancel")}</Button><Button variant="danger" onClick={() => clearMutation.mutate()}>{t("confirm")}</Button></>}><p className="dialog-message">{t("clearLogs")}</p></Dialog>
  </div></div>;
}

function SettingsSection({ tone, title, subtitle, children }: { tone: string; title: string; subtitle: string; children: React.ReactNode }) {
  return <section className={`settings-group settings-group--${tone}`}><header className="settings-group__heading"><div><h2>{title}</h2><p>{subtitle}</p></div></header><div className="settings-section">{children}</div></section>;
}
function SettingRow({ label, description, children, last = false }: { label: React.ReactNode; description: string; children: React.ReactNode; last?: boolean }) {
  return <div className={`setting-row ${last ? "setting-row--last" : ""}`}><span><strong>{label}</strong><small>{description}</small></span>{children}</div>;
}
function LabelWithBadge({ label, badge }: { label: string; badge: string }) { return <span className="setting-label-with-badge">{label}<AdvancedBadge>{badge}</AdvancedBadge></span>; }
