import { DownloadCloud, LockKeyhole, Network, ShieldCheck } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { useState } from "react";
import { connection, coreApi } from "../../shared/api/client";
import type { AuditSettings, BootstrapData, PolicySettings } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { useI18n } from "../../shared/i18n/I18n";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";

export function SettingsView({ data }: { data: BootstrapData }) {
  const { t, locale, setLocale } = useI18n();
  const { notify } = useApp();
  const qc = useQueryClient();
  const [gatewayPort, setGatewayPort] = useState(connection.proxyPort());
  const [audit, setAudit] = useState<AuditSettings>(data.settings);
  const [policy, setPolicy] = useState<PolicySettings>(data.policy);
  const [clear, setClear] = useState(false);
  const [hfBaseURL, setHFBaseURL] = useState(data.settings.hf_base_url || "");

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
      if (!Number.isInteger(port) || port < 1 || port > 65535 || port === 17680) throw new Error(t("gatewayPortInvalid"));
      const previousPort = connection.proxyPort();
      if (port === previousPort) return false;
      if ("__TAURI_INTERNALS__" in window) {
        await invoke("stop_core");
        try {
          await invoke("start_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${port}` });
        } catch (error) {
          await invoke("start_core", { address: new URL(connection.core()).host, proxyAddress: `127.0.0.1:${previousPort}` });
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
    <SettingsSection tone="gateway" icon={<Network/>} title={t("gatewaySettings")} subtitle={t("gatewaySettingsSub")}>
      <SettingRow last label={t("gatewayPort")} description={t("gatewayPortSub")}><Input type="number" min={1} max={65535} value={gatewayPort} onChange={event => setGatewayPort(Number(event.target.value))} onBlur={applyGatewayPort} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="interface" icon={<span className="language-glyph">文</span>} title={t("interfaceSettings")} subtitle={t("interfaceSettingsSub")}>
      <SettingRow last label={t("language")} description={locale === "zh" ? "简体中文" : "English"}><Select value={locale} onValueChange={value => setLocale(value as "zh" | "en")} options={[{ value: "zh", label: "简体中文" }, { value: "en", label: "English" }]}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="models" icon={<DownloadCloud/>} title={t("modelDownloads")} subtitle={t("modelDownloadsSub")}>
      <SettingRow last label={t("hfMirror")} description={t("hfMirrorSub")}><Input placeholder="https://huggingface.co" value={hfBaseURL} onChange={event => setHFBaseURL(event.target.value)} onBlur={applyHFBaseURL} onKeyDown={event => { if (event.key === "Enter") event.currentTarget.blur(); }}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="protection" icon={<ShieldCheck/>} title={t("protectionSettings")} subtitle={t("protectionSettingsSub")}>
      <SettingRow last label={t("redactAIAnswers")} description={t("redactAIAnswersSub")}><Switch ariaLabel={t("redactAIAnswers")} disabled={savePolicy.isPending} checked={policy.redact_ai_answers} onCheckedChange={redact_ai_answers => updatePolicy({ redact_ai_answers })}/></SettingRow>
    </SettingsSection>
    <SettingsSection tone="logs" icon={<LockKeyhole/>} title={t("audit")} subtitle={t("auditSub")}>
      <SettingRow label={t("record")} description={t("recordSub")}><Switch ariaLabel={t("record")} disabled={saveAudit.isPending} checked={audit.record_request_content} onCheckedChange={record_request_content => updateAudit({ record_request_content })}/></SettingRow>
      <SettingRow last label={t("retention")} description={t("retentionSub")}><Select value={String(audit.retention_days)} onValueChange={value => updateAudit({ retention_days: Number(value) })} options={[7, 30, 90, 180].map(value => ({ value: String(value), label: `${value} ${t("days")}` }))}/></SettingRow>
      <div className="settings-actions settings-actions--split"><Button variant="danger" onClick={() => setClear(true)}>{t("clearLogs")}</Button></div>
    </SettingsSection>
    <Dialog open={clear} title={t("confirmClear")} onClose={() => setClear(false)} footer={<><Button onClick={() => setClear(false)}>{t("cancel")}</Button><Button variant="danger" onClick={() => clearMutation.mutate()}>{t("confirm")}</Button></>}><p className="dialog-message">{t("clearLogs")}</p></Dialog>
  </div></div>;
}

function SettingsSection({ tone, icon, title, subtitle, children }: { tone: string; icon: React.ReactNode; title: string; subtitle: string; children: React.ReactNode }) {
  return <section className={`settings-group settings-group--${tone}`}><header className="settings-group__heading"><span>{icon}</span><div><h2>{title}</h2><p>{subtitle}</p></div></header><div className="settings-section">{children}</div></section>;
}
function SettingRow({ label, description, children, last = false }: { label: string; description: string; children: React.ReactNode; last?: boolean }) {
  return <div className={`setting-row ${last ? "setting-row--last" : ""}`}><span><strong>{label}</strong><small>{description}</small></span>{children}</div>;
}
