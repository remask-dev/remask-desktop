import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { disable as disableAutostart, enable as enableAutostart, isEnabled as isAutostartEnabled } from "@tauri-apps/plugin-autostart";
import { useEffect, useState } from "react";
import { connection, coreApi } from "../../shared/api/client";
import type { AuditSettings, PolicySettings } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { queryKeys, useSettingsData } from "../../app/useCore";
import { useI18n } from "../../shared/i18n/I18n";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { PageState } from "../../shared/ui/PageState";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";

const inTauri = "__TAURI_INTERNALS__" in window;

type SettingsData = { settings: AuditSettings; policy: PolicySettings };

export function SettingsView() {
  const query = useSettingsData();
  if (query.isPending || !query.settings || !query.policy) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <SettingsContent data={{ settings: query.settings, policy: query.policy }}/>;
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

  useEffect(() => {
    if (!inTauri) return;
    isAutostartEnabled().then(setAutostart).catch(() => {}).finally(() => setAutostartReady(true));
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
  const saveGatewayPorts = useMutation({
    mutationFn: async ({ apiPort, httpPort }: { apiPort: number; httpPort: number }) => {
      const corePort = Number(new URL(connection.core()).port) || 80;
      if (![apiPort, httpPort].every(port => Number.isInteger(port) && port >= 1 && port <= 65535) || apiPort === httpPort || apiPort === corePort || httpPort === corePort) throw new Error(t("proxyGatewayPortInvalid"));
      const previousAPI = connection.proxyPort();
      const previousHTTP = connection.forwardProxyPort();
      if (apiPort === previousAPI && httpPort === previousHTTP) return { restarted: false, apiPort, httpPort };
      if (inTauri) {
        try {
          await invoke("restart_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${apiPort}`, forwardProxyAddress: `127.0.0.1:${httpPort}` });
        } catch (error) {
          await invoke("restart_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${previousAPI}`, forwardProxyAddress: `127.0.0.1:${previousHTTP}` });
          throw error;
        }
      }
      return { restarted: inTauri, apiPort, httpPort };
    },
    onSuccess: ({ restarted, apiPort, httpPort }) => {
      connection.saveGatewayPort(apiPort);
      connection.saveForwardProxyPort(httpPort);
      window.setTimeout(() => void qc.invalidateQueries({ queryKey: queryKeys.root }), restarted ? 700 : 0);
      notify(t(restarted ? "settingsSavedRestarted" : "settingsSaved"));
    },
    onError: error => {
      setAPIGatewayPort(connection.proxyPort());
      setHTTPProxyPort(connection.forwardProxyPort());
      notify(String(error));
    },
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
  function applyGatewayPorts() { if (!saveGatewayPorts.isPending) saveGatewayPorts.mutate({ apiPort: apiGatewayPort, httpPort: httpProxyPort }); }

  return <div className="settings-page"><div className="settings-grid">
    <SettingsSection tone="interface" title={t("applicationSettings")} subtitle={t("applicationSettingsSub")}>
      <SettingRow label={t("language")} description={locale === "zh" ? "简体中文" : "English"}><Select value={locale} onValueChange={value => setLocale(value as "zh" | "en")} options={[{ value: "zh", label: "简体中文" }, { value: "en", label: "English" }]}/></SettingRow>
      {inTauri && <SettingRow last label={t("autostart")} description={t("autostartSub")}><Switch ariaLabel={t("autostart")} disabled={!autostartReady} checked={autostart} onCheckedChange={toggleAutostart}/></SettingRow>}
    </SettingsSection>
    <SettingsSection tone="gateway" title={t("gatewaySettings")} subtitle={t("gatewaySettingsSub")}>
      <SettingRow label={t("apiGatewayPort")} description={t("apiGatewayPortSub")}><Input type="number" min={1} max={65535} value={apiGatewayPort} onChange={event => setAPIGatewayPort(Number(event.target.value))} onBlur={applyGatewayPorts} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow last label={t("proxyGatewayPort")} description={t("proxyGatewayPortSub")}><Input type="number" min={1} max={65535} value={httpProxyPort} onChange={event => setHTTPProxyPort(Number(event.target.value))} onBlur={applyGatewayPorts} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="models" title={t("modelSettings")} subtitle={t("modelSettingsSub")}>
      <SettingRow label={t("hfMirror")} description={t("hfMirrorSub")}><Input placeholder="https://huggingface.co" value={hfBaseURL} onChange={event => setHFBaseURL(event.target.value)} onBlur={applyHFBaseURL} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow label={t("maxInferenceTokens")} description={t("maxInferenceTokensSub")}><Select value={String(audit.max_inference_tokens || 512)} onValueChange={value => updateAudit({ max_inference_tokens: Number(value) })} options={[512, 1024, 2048, 4096].map(value => ({ value: String(value), label: `${value} ${t("tokens")}` }))}/></SettingRow>
      <SettingRow last label={t("inferenceProvider")} description={t("inferenceProviderSub")}><Select value={audit.inference_provider || "cpu"} onValueChange={value => updateAudit({ inference_provider: value as AuditSettings["inference_provider"] })} options={[{ value: "auto", label: t("providerAuto") }, { value: "cpu", label: t("providerCPU") }, { value: "gpu", label: t("providerGPU") }]}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="protection" title={t("redactionSettings")} subtitle={t("redactionSettingsSub")}>
      <SettingRow label={t("redactSystemMessages")} description={t("redactSystemMessagesSub")}><Switch ariaLabel={t("redactSystemMessages")} disabled={savePolicy.isPending} checked={policy.redact_system_messages} onCheckedChange={redact_system_messages => updatePolicy({ redact_system_messages })}/></SettingRow>
      <SettingRow label={t("redactAIAnswers")} description={t("redactAIAnswersSub")}><Switch ariaLabel={t("redactAIAnswers")} disabled={savePolicy.isPending} checked={policy.redact_ai_answers} onCheckedChange={redact_ai_answers => updatePolicy({ redact_ai_answers })}/></SettingRow>
      <SettingRow label={t("entityCache")} description={t("entityCacheSub")}><Switch ariaLabel={t("entityCache")} disabled={saveAudit.isPending} checked={audit.entity_cache_enabled !== false} onCheckedChange={entity_cache_enabled => updateAudit({ entity_cache_enabled })}/></SettingRow>
      <SettingRow last label={t("entityCacheTTL")} description={t("entityCacheTTLSub")}><Select value={String(audit.entity_cache_ttl_seconds || 900)} onValueChange={value => updateAudit({ entity_cache_ttl_seconds: Number(value) })} options={[60, 300, 900, 3600].map(value => ({ value: String(value), label: `${value < 3600 ? value / 60 + " min" : "1 h"}` }))}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="logs" title={t("audit")} subtitle={t("auditSub")}>
      <SettingRow label={t("record")} description={t("recordSub")}><Switch ariaLabel={t("record")} disabled={saveAudit.isPending} checked={audit.record_request_content} onCheckedChange={record_request_content => updateAudit({ record_request_content })}/></SettingRow>
      <SettingRow last label={t("retention")} description={t("retentionSub")}><Select value={String(audit.retention_days)} onValueChange={value => updateAudit({ retention_days: Number(value) })} options={[7, 30, 90, 180].map(value => ({ value: String(value), label: `${value} ${t("days")}` }))}/></SettingRow>
      <div className="settings-actions settings-actions--split"><Button variant="danger" onClick={() => setClear(true)}>{t("clearLogs")}</Button></div>
    </SettingsSection>
    <Dialog open={clear} title={t("confirmClear")} onClose={() => setClear(false)} footer={<><Button onClick={() => setClear(false)}>{t("cancel")}</Button><Button variant="danger" onClick={() => clearMutation.mutate()}>{t("confirm")}</Button></>}><p className="dialog-message">{t("clearLogs")}</p></Dialog>
  </div></div>;
}

function SettingsSection({ tone, title, subtitle, children }: { tone: string; title: string; subtitle: string; children: React.ReactNode }) {
  return <section className={`settings-group settings-group--${tone}`}><header className="settings-group__heading"><div><h2>{title}</h2><p>{subtitle}</p></div></header><div className="settings-section">{children}</div></section>;
}
function SettingRow({ label, description, children, last = false }: { label: string; description: string; children: React.ReactNode; last?: boolean }) {
  return <div className={`setting-row ${last ? "setting-row--last" : ""}`}><span><strong>{label}</strong><small>{description}</small></span>{children}</div>;
}
