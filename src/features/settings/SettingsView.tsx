import { useMutation, useQueryClient } from "@tanstack/react-query";
import { disable as disableAutostart, enable as enableAutostart, isEnabled as isAutostartEnabled } from "@tauri-apps/plugin-autostart";
import { invoke } from "@tauri-apps/api/core";
import { Bot, Terminal } from "lucide-react";
import { useEffect, useState } from "react";
import { connection, coreApi } from "../../shared/api/client";
import type { AuditSettings, BootstrapData, PolicySettings, Upstream } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { useI18n } from "../../shared/i18n/I18n";
import type { MessageKey } from "../../shared/i18n/messages";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";

const inTauri = "__TAURI_INTERNALS__" in window;
type SystemCertificateStatus = { supported: boolean; installed: boolean; platform: string };
type AIClient = "claude" | "codex";

export function SettingsView({ data }: { data: BootstrapData }) {
  const { t, locale, setLocale } = useI18n();
  const { notify } = useApp();
  const qc = useQueryClient();
  const [gatewayPort, setGatewayPort] = useState(connection.proxyPort());
  const [audit, setAudit] = useState<AuditSettings>(data.settings);
  const [policy, setPolicy] = useState<PolicySettings>(data.policy);
  const [clear, setClear] = useState(false);
  const [hfBaseURL, setHFBaseURL] = useState(data.settings.hf_base_url || "");
  const [autostart, setAutostart] = useState(false);
  const [autostartReady, setAutostartReady] = useState(false);
  const [certificateStatus, setCertificateStatus] = useState<SystemCertificateStatus | null>(null);
  const [confirmCertificateInstall, setConfirmCertificateInstall] = useState(false);

  useEffect(() => {
    if (!inTauri) return;
    isAutostartEnabled().then(setAutostart).catch(() => {}).finally(() => setAutostartReady(true));
    invoke<SystemCertificateStatus>("system_certificate_status").then(setCertificateStatus).catch(() => {
      setCertificateStatus({ supported: false, installed: false, platform: "unknown" });
    });
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
    mutationFn: (next: PolicySettings) => coreApi.savePolicy(next),
    onSuccess: saved => { setPolicy(saved); qc.invalidateQueries(); },
    onError: error => { setPolicy(data.policy); notify(String(error)); },
  });
  const saveAudit = useMutation({
    mutationFn: (next: AuditSettings) => coreApi.saveSettings(next),
    onSuccess: result => { setAudit(result.audit); qc.invalidateQueries(); },
    onError: error => { setAudit(data.settings); notify(String(error)); },
  });
  const saveGateway = useMutation({
    mutationFn: async (port: number) => {
      if (!Number.isInteger(port) || port < 1 || port > 65535 || port === 17680 || port === connection.forwardProxyPort()) throw new Error(t("gatewayPortInvalid"));
      const previousPort = connection.proxyPort();
      if (port === previousPort) return false;
      if ("__TAURI_INTERNALS__" in window) {
        await invoke("stop_core");
        try {
          await invoke("start_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${port}`, forwardProxyAddress: new URL(connection.forwardProxy()).host });
        } catch (error) {
          await invoke("start_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${previousPort}`, forwardProxyAddress: new URL(connection.forwardProxy()).host });
          throw error;
        }
      }
      connection.saveGatewayPort(port);
      return "__TAURI_INTERNALS__" in window;
    },
    onSuccess: restarted => { window.setTimeout(() => qc.invalidateQueries(), restarted ? 700 : 0); notify(t(restarted ? "settingsSavedRestarted" : "settingsSaved")); },
    onError: error => { setGatewayPort(connection.proxyPort()); notify(String(error)); },
  });
  const clearMutation = useMutation({ mutationFn: coreApi.clearLogs, onSuccess: () => { setClear(false); qc.invalidateQueries(); } });
  const installCertificate = useMutation({
    mutationFn: () => invoke<SystemCertificateStatus>("install_system_certificate"),
    onSuccess: status => { setCertificateStatus(status); setConfirmCertificateInstall(false); notify(t("certificateInstalled")); },
    onError: error => notify(String(error)),
  });
  const launchClient = useMutation({
    mutationFn: async (client: AIClient) => {
      await ensureClientUpstreams(client, data.upstreams);
      await invoke("launch_ai_client", { client, forwardProxyAddress: new URL(connection.forwardProxy()).host });
    },
    onSuccess: () => { qc.invalidateQueries(); notify(t("clientLaunched")); },
    onError: error => notify(String(error)),
  });

  function updatePolicy(patch: Partial<PolicySettings>) {
    const next = { ...policy, ...patch };
    setPolicy(next);
    savePolicy.mutate(next);
  }
  function updateAudit(patch: Partial<AuditSettings>) {
    const next = { ...audit, ...patch };
    setAudit(next);
    saveAudit.mutate(next);
  }
  function applyGatewayPort() { if (!saveGateway.isPending) saveGateway.mutate(gatewayPort); }
  function applyHFBaseURL() { const next = { ...audit, hf_base_url: hfBaseURL.trim() }; setAudit(next); saveAudit.mutate(next); }

  return <div className="settings-page"><div className="settings-grid">
    <SettingsSection tone="gateway" title={t("gatewaySettings")} subtitle={t("gatewaySettingsSub")}>
      <SettingRow last label={t("gatewayPort")} description={t("gatewayPortSub")}><Input type="number" min={1} max={65535} value={gatewayPort} onChange={event => setGatewayPort(Number(event.target.value))} onBlur={applyGatewayPort} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
    </SettingsSection>
    {inTauri && <SettingsSection tone="integration" title={t("systemIntegration")} subtitle={t("systemIntegrationSub")}>
      <SettingRow label={t("systemCertificate")} description={certificateStatusDescription(certificateStatus, t)}><div className="integration-actions"><span className={`integration-status ${certificateStatus?.installed ? "integration-status--ready" : ""}`}>{certificateStatus?.installed ? t("certificateInstalledStatus") : t("certificateNotInstalledStatus")}</span><Button variant="primary" disabled={!certificateStatus?.supported || certificateStatus.installed || installCertificate.isPending} onClick={() => setConfirmCertificateInstall(true)}>{certificateStatus?.installed ? t("certificateInstalledStatus") : t("installCertificate")}</Button></div></SettingRow>
      <SettingRow last label={t("quickLaunch")} description={t("quickLaunchSub")}><div className="integration-actions integration-actions--clients"><Button icon={<Bot size={14}/>} disabled={launchClient.isPending} onClick={() => launchClient.mutate("claude")}>{t("launchClaude")}</Button><Button icon={<Terminal size={14}/>} disabled={launchClient.isPending} onClick={() => launchClient.mutate("codex")}>{t("launchCodex")}</Button></div></SettingRow>
    </SettingsSection>}
    <SettingsSection tone="interface" title={t("interfaceSettings")} subtitle={t("interfaceSettingsSub")}>
      <SettingRow label={t("language")} description={locale === "zh" ? "简体中文" : "English"}><Select value={locale} onValueChange={value => setLocale(value as "zh" | "en")} options={[{ value: "zh", label: "简体中文" }, { value: "en", label: "English" }]}/></SettingRow>
      {inTauri && <SettingRow last label={t("autostart")} description={t("autostartSub")}><Switch ariaLabel={t("autostart")} disabled={!autostartReady} checked={autostart} onCheckedChange={toggleAutostart}/></SettingRow>}
    </SettingsSection>
    <SettingsSection tone="models" title={t("modelDownloads")} subtitle={t("modelDownloadsSub")}>
      <SettingRow label={t("hfMirror")} description={t("hfMirrorSub")}><Input placeholder="https://huggingface.co" value={hfBaseURL} onChange={event => setHFBaseURL(event.target.value)} onBlur={applyHFBaseURL} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
      <SettingRow label={t("maxInferenceTokens")} description={t("maxInferenceTokensSub")}><Select value={String(audit.max_inference_tokens || 512)} onValueChange={value => updateAudit({ max_inference_tokens: Number(value) })} options={[512, 1024, 2048, 4096].map(value => ({ value: String(value), label: `${value} ${t("tokens")}` }))}/></SettingRow>
      <SettingRow last label={t("inferenceProvider")} description={t("inferenceProviderSub")}><Select value={audit.inference_provider || "cpu"} onValueChange={value => updateAudit({ inference_provider: value as AuditSettings["inference_provider"] })} options={[{ value: "auto", label: t("providerAuto") }, { value: "cpu", label: t("providerCPU") }, { value: "gpu", label: t("providerGPU") }]}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="protection" title={t("protectionSettings")} subtitle={t("protectionSettingsSub")}>
      <SettingRow label={t("redactAIAnswers")} description={t("redactAIAnswersSub")}><Switch ariaLabel={t("redactAIAnswers")} disabled={savePolicy.isPending} checked={policy.redact_ai_answers} onCheckedChange={redact_ai_answers => updatePolicy({ redact_ai_answers })}/></SettingRow>
      <SettingRow label={t("entityCache")} description={t("entityCacheSub")}><Switch ariaLabel={t("entityCache")} disabled={saveAudit.isPending} checked={audit.entity_cache_enabled !== false} onCheckedChange={entity_cache_enabled => updateAudit({ entity_cache_enabled })}/></SettingRow>
      <SettingRow last label={t("entityCacheTTL")} description={t("entityCacheTTLSub")}><Select value={String(audit.entity_cache_ttl_seconds || 300)} onValueChange={value => updateAudit({ entity_cache_ttl_seconds: Number(value) })} options={[60, 300, 900, 3600].map(value => ({ value: String(value), label: `${value < 3600 ? value / 60 + " min" : "1 h"}` }))}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="logs" title={t("audit")} subtitle={t("auditSub")}>
      <SettingRow label={t("record")} description={t("recordSub")}><Switch ariaLabel={t("record")} disabled={saveAudit.isPending} checked={audit.record_request_content} onCheckedChange={record_request_content => updateAudit({ record_request_content })}/></SettingRow>
      <SettingRow last label={t("retention")} description={t("retentionSub")}><Select value={String(audit.retention_days)} onValueChange={value => updateAudit({ retention_days: Number(value) })} options={[7, 30, 90, 180].map(value => ({ value: String(value), label: `${value} ${t("days")}` }))}/></SettingRow>
      <div className="settings-actions settings-actions--split"><Button variant="danger" onClick={() => setClear(true)}>{t("clearLogs")}</Button></div>
    </SettingsSection>
    <Dialog open={clear} title={t("confirmClear")} onClose={() => setClear(false)} footer={<><Button onClick={() => setClear(false)}>{t("cancel")}</Button><Button variant="danger" onClick={() => clearMutation.mutate()}>{t("confirm")}</Button></>}><p className="dialog-message">{t("clearLogs")}</p></Dialog>
    <Dialog open={confirmCertificateInstall} title={t("certificateInstallConfirm")} onClose={() => setConfirmCertificateInstall(false)} footer={<><Button onClick={() => setConfirmCertificateInstall(false)}>{t("cancel")}</Button><Button variant="primary" disabled={installCertificate.isPending} onClick={() => installCertificate.mutate()}>{t("installCertificate")}</Button></>}><p className="dialog-message">{t("certificateInstallDescription")}</p>{data.proxyCA.fingerprint_sha256 && <code className="certificate-fingerprint">SHA-256 {data.proxyCA.fingerprint_sha256}</code>}</Dialog>
  </div></div>;
}

function certificateStatusDescription(status: SystemCertificateStatus | null, t: (key: MessageKey) => string) {
  if (!status) return t("certificateChecking");
  if (!status.supported) return t("certificateUnsupported");
  return status.installed ? t("certificateInstalledSub") : t("certificateNotInstalledSub");
}

const clientUpstreams: Record<AIClient, Array<Pick<Upstream, "id" | "base_url" | "profile_id">>> = {
  claude: [{ id: "anthropic", base_url: "https://api.anthropic.com", profile_id: "anthropic" }],
  codex: [
    { id: "openai", base_url: "https://api.openai.com", profile_id: "openai" },
    { id: "chatgpt-codex", base_url: "https://chatgpt.com", profile_id: "codex-chatgpt" },
  ],
};

async function ensureClientUpstreams(client: AIClient, configured: Upstream[]) {
  const pending = [...configured];
  for (const required of clientUpstreams[client]) {
    const requiredHost = upstreamHost(required.base_url);
    if (pending.some(item => upstreamHost(item.base_url) === requiredHost)) continue;
    const id = availableUpstreamId(required.id, pending);
    const item: Upstream = { ...required, id, credential_mode: "passthrough" };
    await coreApi.putUpstream(item, false);
    pending.push(item);
  }
}

function upstreamHost(baseUrl: string) {
  try { return new URL(baseUrl).hostname.toLowerCase(); } catch { return ""; }
}

function availableUpstreamId(preferred: string, configured: Upstream[]) {
  const used = new Set(configured.map(item => item.id));
  if (!used.has(preferred)) return preferred;
  let suffix = 2;
  while (used.has(`${preferred}-${suffix}`)) suffix += 1;
  return `${preferred}-${suffix}`;
}

function SettingsSection({ tone, title, subtitle, children }: { tone: string; title: string; subtitle: string; children: React.ReactNode }) {
  return <section className={`settings-group settings-group--${tone}`}><header className="settings-group__heading"><div><h2>{title}</h2><p>{subtitle}</p></div></header><div className="settings-section">{children}</div></section>;
}
function SettingRow({ label, description, children, last = false }: { label: string; description: string; children: React.ReactNode; last?: boolean }) {
  return <div className={`setting-row ${last ? "setting-row--last" : ""}`}><span><strong>{label}</strong><small>{description}</small></span>{children}</div>;
}
