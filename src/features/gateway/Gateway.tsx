import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { open } from "@tauri-apps/plugin-dialog";
import { AppWindow, Bot, Copy, KeyRound, Network, Plus, Server, ShieldCheck, Terminal } from "lucide-react";
import { useEffect, useState } from "react";
import { connection, coreApi } from "../../shared/api/client";
import type { Profile, ProxyCAStatus, ProxyRule, Upstream } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { queryKeys, useGatewayData } from "../../app/useCore";
import { useI18n } from "../../shared/i18n/I18n";
import type { MessageKey } from "../../shared/i18n/messages";
import { Badge } from "../../shared/ui/Status";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { PageState } from "../../shared/ui/PageState";
import { Select } from "../../shared/ui/Select";
import { Switch } from "../../shared/ui/Switch";

const inTauri = "__TAURI_INTERNALS__" in window;
const blankProvider: Upstream = { id: "", base_url: "", profile_id: "openai", credential_mode: "passthrough", enabled: true };
const blankProxyRule: ProxyRule = { id: "", hosts: [""], port: 443, profile_id: "openai", enabled: true };
type GatewayData = { upstreams: Upstream[]; profiles: Profile[]; proxyCA: ProxyCAStatus; proxyRules: ProxyRule[] };
type SystemCertificateStatus = { supported: boolean; installed: boolean; platform: string };
type AIClient = "claude" | "codex";

export function Gateway() {
  const query = useGatewayData();
  if (query.isPending || !query.upstreams || !query.profiles || !query.proxyCA || !query.proxyRules) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <GatewayContent data={{ upstreams: query.upstreams, profiles: query.profiles, proxyCA: query.proxyCA, proxyRules: query.proxyRules }}/>;
}

function GatewayContent({ data }: { data: GatewayData }) {
  const { t } = useI18n();
  const [tab, setTab] = useState<"api" | "http">("api");
  return <div className="gateway-page">
    <header className="page-header gateway-header">
      <div className="gateway-header__title"><h1>{t("gatewayTitle")}</h1><p>{t(tab === "api" ? "apiGatewayDescription" : "httpProxyDescription")}</p></div>
      <div className="gateway-tabs" role="tablist" aria-label={t("gatewayTitle")}>
        <button id="gateway-tab-api" role="tab" aria-controls="gateway-panel" aria-selected={tab === "api"} className={tab === "api" ? "active" : ""} onClick={() => setTab("api")}><Server size={15}/><strong>{t("apiGateway")}</strong></button>
        <button id="gateway-tab-http" role="tab" aria-controls="gateway-panel" aria-selected={tab === "http"} className={tab === "http" ? "active" : ""} onClick={() => setTab("http")}><Network size={15}/><strong>{t("httpProxyGateway")}</strong></button>
      </div>
    </header>
    <div id="gateway-panel" className="gateway-stage" role="tabpanel" aria-labelledby={tab === "api" ? "gateway-tab-api" : "gateway-tab-http"}>{tab === "api" ? <APIGateway data={data}/> : <HTTPProxyGateway data={data}/>}</div>
  </div>;
}

function APIGateway({ data }: { data: GatewayData }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient();
  const [selected, setSelected] = useState(data.upstreams[0]?.id || "");
  const [editing, setEditing] = useState<Upstream | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);
  const current = data.upstreams.find(item => item.id === selected) || data.upstreams[0];
  const remove = useMutation({ mutationFn: coreApi.deleteUpstream, onSuccess: () => { setConfirm(null); setSelected(""); void qc.invalidateQueries({ queryKey: queryKeys.upstreams }); }, onError: error => notify(String(error)) });
  const toggle = useMutation({ mutationFn: (item: Upstream) => coreApi.putUpstream({ ...item, enabled: !item.enabled }, true), onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.upstreams }), onError: error => notify(String(error)) });
  async function copy(value: string) { await navigator.clipboard.writeText(value); notify(t("copied")); }
  return <div className="gateway-workspace">
    <GatewayEndpoint icon={<Server size={15}/>} title={t("apiGateway")} value={connection.proxy()} onCopy={copy}/>
    <div className="split-view gateway-split"><section className="list-pane"><div className="pane-title"><div><span>{t("providers")}</span><small>{data.upstreams.length}</small></div><Button variant="primary" icon={<Plus size={13}/>} onClick={() => setEditing({ ...blankProvider })}>{t("addProvider")}</Button></div><div className="service-list gateway-entity-list">{data.upstreams.map(item => <div key={item.id} className={`gateway-entity-row ${current?.id === item.id ? "selected" : ""} ${item.enabled ? "" : "disabled"}`}><button className="gateway-entity-select" onClick={() => setSelected(item.id)}><span className="service-icon"><Server size={15}/></span><span><strong>{item.id}</strong><code>{item.base_url}</code><small>{item.profile_id}</small></span></button><Switch ariaLabel={`${item.id} ${t("enabled")}`} disabled={toggle.isPending} checked={item.enabled} onCheckedChange={() => toggle.mutate(item)}/></div>)}</div></section><section className="detail-pane">{current ? <><header className="detail-header"><div><span>{t("provider")}</span><h2>{current.id}</h2><code>{current.base_url}</code></div><div className="header-actions"><Button onClick={() => setEditing({ ...current })}>{t("edit")}</Button><Button variant="danger" onClick={() => setConfirm(current.id)}>{t("remove")}</Button></div></header><section className="detail-section gateway-options"><h3>{t("apiAccessAddress")}</h3><GatewayAddress label={t("sdkBaseUrl")} value={`${connection.proxy()}/proxy/${current.id}`} onCopy={copy}/><p>{t("apiAccessNote")}</p></section><section className="detail-section detail-section--summary"><h3>{t("connectionConfig")}</h3><dl className="property-grid"><div><dt>{t("profile")}</dt><dd>{current.profile_id}</dd></div><div><dt>{t("credential")}</dt><dd>{current.credential_mode === "managed" ? t("managed") : t("pass")}</dd></div></dl></section></> : <GatewayEmpty icon={<Server size={24}/>} title={t("addProvider")} description={t("providerEmpty")} action={() => setEditing({ ...blankProvider })}/>}</section></div>
    <ProviderDialog item={editing} data={data} onClose={() => setEditing(null)}/>
    <Dialog open={!!confirm} title={t("confirmDeleteProvider")} onClose={() => setConfirm(null)} footer={<><Button onClick={() => setConfirm(null)}>{t("cancel")}</Button><Button variant="danger" onClick={() => confirm && remove.mutate(confirm)}>{t("confirm")}</Button></>}><p className="dialog-message">{confirm}</p></Dialog>
  </div>;
}

function HTTPProxyGateway({ data }: { data: GatewayData }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient();
  const [selected, setSelected] = useState(data.proxyRules[0]?.id || "");
  const [editing, setEditing] = useState<ProxyRule | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [certificateStatus, setCertificateStatus] = useState<SystemCertificateStatus | null>(() => inTauri ? null : { supported: false, installed: false, platform: "browser" });
  const [confirmCertificateInstall, setConfirmCertificateInstall] = useState(false);
  const current = data.proxyRules.find(item => item.id === selected) || data.proxyRules[0];
  useEffect(() => { if (!inTauri) return; invoke<SystemCertificateStatus>("system_certificate_status").then(setCertificateStatus).catch(() => setCertificateStatus({ supported: false, installed: false, platform: "unknown" })); }, []);
  const remove = useMutation({ mutationFn: coreApi.deleteProxyRule, onSuccess: () => { setConfirm(null); setSelected(""); void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }); }, onError: error => notify(String(error)) });
  const toggle = useMutation({ mutationFn: (item: ProxyRule) => coreApi.putProxyRule({ ...item, enabled: !item.enabled }, true), onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }), onError: error => notify(String(error)) });
  const installCertificate = useMutation({ mutationFn: () => invoke<SystemCertificateStatus>("install_system_certificate"), onSuccess: status => { setCertificateStatus(status); setConfirmCertificateInstall(false); notify(t("certificateInstalled")); }, onError: error => notify(String(error)) });
  const launchClient = useMutation({ mutationFn: async (client: AIClient) => { await ensureClientProxyRules(client, data.proxyRules); await invoke("launch_ai_client", { client, forwardProxyAddress: new URL(connection.forwardProxy()).host }); }, onSuccess: () => { void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }); notify(t("clientLaunched")); }, onError: error => notify(String(error)) });
  const launchApp = useMutation({ mutationFn: async () => { const appPath = await open({ multiple: false, directory: false, title: t("selectApplication") }); if (!appPath) return false; await invoke("launch_app_with_proxy", { appPath, forwardProxyAddress: new URL(connection.forwardProxy()).host }); return true; }, onSuccess: launched => { if (launched) notify(t("appLaunched")); }, onError: error => notify(String(error)) });
  async function copy(value: string) { await navigator.clipboard.writeText(value); notify(t("copied")); }
  const env = `HTTP_PROXY=${connection.forwardProxy()}\nHTTPS_PROXY=${connection.forwardProxy()}`;
  return <div className="gateway-workspace">
    <GatewayEndpoint icon={<Network size={15}/>} title={t("httpProxyGateway")} value={connection.forwardProxy()} onCopy={copy} aside={<div className="gateway-endpoint__status"><span className="status-dot success"/>{t("transparentFallback")}</div>}/>
    <section className="proxy-integrations">
      <div className="proxy-certificate-row">
        <div className="proxy-integration__identity"><span className="proxy-integration__icon"><ShieldCheck size={15}/></span><span><strong>{t("systemCertificate")}</strong><small>{certificateStatusDescription(certificateStatus, t)}</small></span></div>
        <div className="proxy-certificate-actions"><Badge tone={certificateStatus?.installed ? "success" : "warning"}>{certificateStatus?.installed ? t("certificateInstalledStatus") : t("certificateNotInstalledStatus")}</Badge>{inTauri && <Button disabled={!certificateStatus?.supported || certificateStatus.installed} onClick={() => setConfirmCertificateInstall(true)}>{t("installCertificate")}</Button>}</div>
      </div>
      <div className="proxy-tools-row">
        <button className="proxy-env-action" onClick={() => void copy(env)}><Copy size={13}/><span><strong>{t("copyProxyEnv")}</strong><code>HTTP_PROXY / HTTPS_PROXY</code></span></button>
        {inTauri && <div className="proxy-launch-actions"><Button icon={<Bot size={13}/>} disabled={launchClient.isPending} onClick={() => launchClient.mutate("claude")}>{t("launchClaude")}</Button><Button icon={<Terminal size={13}/>} disabled={launchClient.isPending} onClick={() => launchClient.mutate("codex")}>{t("launchCodex")}</Button><Button icon={<AppWindow size={13}/>} disabled={launchApp.isPending} onClick={() => launchApp.mutate()}>{t("selectAndLaunchApp")}</Button></div>}
      </div>
    </section>
    <div className="split-view gateway-split"><section className="list-pane"><div className="pane-title"><div><span>{t("protectedTargets")}</span><small>{data.proxyRules.length}</small></div><Button variant="primary" icon={<Plus size={13}/>} onClick={() => setEditing({ ...blankProxyRule, hosts: [...blankProxyRule.hosts] })}>{t("addProtectedTarget")}</Button></div><div className="service-list gateway-entity-list">{data.proxyRules.map(item => <div key={item.id} className={`gateway-entity-row ${current?.id === item.id ? "selected" : ""} ${item.enabled ? "" : "disabled"}`}><button className="gateway-entity-select" onClick={() => setSelected(item.id)}><span className="service-icon"><ShieldCheck size={15}/></span><span><strong>{item.id}</strong><code>{item.hosts.join(", ")}{item.port ? `:${item.port}` : ""}</code><small>{item.profile_id}</small></span></button><Switch ariaLabel={`${item.id} ${t("enableProtection")}`} disabled={toggle.isPending} checked={item.enabled} onCheckedChange={() => toggle.mutate(item)}/></div>)}</div></section><section className="detail-pane">{current ? <><header className="detail-header"><div><span>{t("protectedTarget")}</span><h2>{current.id}</h2><code>{current.hosts.join(", ")}{current.port ? `:${current.port}` : ""}</code></div><div className="header-actions"><Button onClick={() => setEditing({ ...current, hosts: [...current.hosts] })}>{t("edit")}</Button><Button variant="danger" onClick={() => setConfirm(current.id)}>{t("remove")}</Button></div></header><section className="detail-section"><h3>{t("matchingScope")}</h3><dl className="property-grid"><div><dt>{t("targetHost")}</dt><dd>{current.hosts.join(", ")}</dd></div><div><dt>{t("targetPort")}</dt><dd>{current.port || t("anyPort")}</dd></div><div><dt>{t("profile")}</dt><dd>{current.profile_id}</dd></div><div><dt>{t("tlsInspection")}</dt><dd className={current.enabled ? "success" : "warning"}>{current.enabled ? t("enabled") : t("disabledStatus")}</dd></div></dl></section><section className="detail-section proxy-behavior"><h3>{t("proxyBehavior")}</h3><p>{t("proxyBehaviorNote")}</p></section></> : <GatewayEmpty icon={<ShieldCheck size={24}/>} title={t("addProtectedTarget")} description={t("protectedTargetEmpty")} action={() => setEditing({ ...blankProxyRule, hosts: [...blankProxyRule.hosts] })}/>}</section></div>
    <ProxyRuleDialog item={editing} data={data} onClose={() => setEditing(null)}/>
    <Dialog open={!!confirm} title={t("confirmDeleteTarget")} onClose={() => setConfirm(null)} footer={<><Button onClick={() => setConfirm(null)}>{t("cancel")}</Button><Button variant="danger" onClick={() => confirm && remove.mutate(confirm)}>{t("confirm")}</Button></>}><p className="dialog-message">{confirm}</p></Dialog>
    <Dialog open={confirmCertificateInstall} title={t("certificateInstallConfirm")} onClose={() => setConfirmCertificateInstall(false)} footer={<><Button onClick={() => setConfirmCertificateInstall(false)}>{t("cancel")}</Button><Button variant="primary" disabled={installCertificate.isPending} onClick={() => installCertificate.mutate()}>{t("installCertificate")}</Button></>}><p className="dialog-message">{t("certificateInstallDescription")}</p>{data.proxyCA.fingerprint_sha256 && <code className="certificate-fingerprint">SHA-256 {data.proxyCA.fingerprint_sha256}</code>}</Dialog>
  </div>;
}

function GatewayEndpoint({ icon, title, value, onCopy, aside }: { icon: React.ReactNode; title: string; value: string; onCopy: (value: string) => void; aside?: React.ReactNode }) {
  return <section className="gateway-endpoint"><span className="gateway-endpoint__icon">{icon}</span><div><span>{title}</span><button onClick={() => onCopy(value)}><code>{value}</code><Copy size={13}/></button></div>{aside && <aside>{aside}</aside>}</section>;
}

function GatewayAddress({ label, value, onCopy }: { label: string; value: string; onCopy: (value: string) => void }) { return <button className="copy-field copy-field--labeled" onClick={() => onCopy(value)}><span><small>{label}</small><code>{value}</code></span><Copy size={14}/></button>; }
function GatewayEmpty({ icon, title, description, action }: { icon: React.ReactNode; title: string; description: string; action: () => void }) { return <div className="detail-empty"><span className="detail-empty__icon">{icon}</span><h2>{title}</h2><p>{description}</p><Button variant="primary" onClick={action}>{title}</Button></div>; }

function ProviderDialog({ item, data, onClose }: { item: Upstream | null; data: GatewayData; onClose: () => void }) {
  const { t } = useI18n(); const qc = useQueryClient(); const editing = !!item && data.upstreams.some(value => value.id === item.id); const [form, setForm] = useState<Upstream>(item || blankProvider);
  useEffect(() => { setForm(item ? { ...item } : { ...blankProvider }); }, [item]);
  const profile = data.profiles.find(value => value.id === form.profile_id);
  const save = useMutation({ mutationFn: () => coreApi.putUpstream(form, editing), onSuccess: () => { void qc.invalidateQueries({ queryKey: queryKeys.upstreams }); onClose(); } });
  if (!item) return null;
  return <Dialog open title={editing ? t("editProvider") : t("addProvider")} description={t("credentialSafety")} onClose={onClose} footer={<><Button onClick={onClose}>{t("cancel")}</Button><Button variant="primary" onClick={() => save.mutate()} disabled={!form.id || !form.base_url}>{t("save")}</Button></>}><div className="form-stack"><Field label={t("providerId")}><Input value={form.id} disabled={editing} onChange={event => setForm({ ...form, id: event.target.value })}/></Field><Field label={t("upstreamUrl")}><Input type="url" value={form.base_url} onChange={event => setForm({ ...form, base_url: event.target.value })}/></Field><Field label={t("protocolProfile")}><Select value={form.profile_id} onValueChange={profile_id => setForm({ ...form, profile_id })} options={data.profiles.map(value => ({ value: value.id, label: value.name }))}/></Field><Field label={t("credential")}><Select value={form.credential_mode} onValueChange={value => setForm({ ...form, credential_mode: value as Upstream["credential_mode"], api_key: value === "managed" ? form.api_key : undefined })} options={[{ value: "passthrough", label: t("pass") }, { value: "managed", label: t("managed") }]}/></Field>{form.credential_mode === "managed" && <Field label={t("apiKey")}><Input type="password" value={form.api_key === "••••••••" ? "" : form.api_key || ""} onChange={event => setForm({ ...form, api_key: event.target.value })} placeholder={profile?.header_templates ? Object.values(profile.header_templates).find(value => value.includes("{{api_key}}")) || "sk-…" : "sk-…"}/></Field>}<label className="field field--switch"><span>{t("enableProvider")}</span><Switch ariaLabel={t("enableProvider")} checked={form.enabled} onCheckedChange={enabled => setForm({ ...form, enabled })}/></label><div className="credential-note"><KeyRound size={14}/>{t("credentialTemplateNote")}</div></div></Dialog>;
}

function ProxyRuleDialog({ item, data, onClose }: { item: ProxyRule | null; data: GatewayData; onClose: () => void }) {
  const { t } = useI18n(); const qc = useQueryClient(); const editing = !!item && data.proxyRules.some(value => value.id === item.id); const [form, setForm] = useState<ProxyRule>(item || blankProxyRule);
  useEffect(() => { setForm(item ? { ...item, hosts: [...item.hosts] } : { ...blankProxyRule, hosts: [...blankProxyRule.hosts] }); }, [item]);
  const save = useMutation({ mutationFn: () => coreApi.putProxyRule({ ...form, hosts: form.hosts.map(host => host.trim()).filter(Boolean) }, editing), onSuccess: () => { void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }); onClose(); } });
  if (!item) return null;
  return <Dialog open title={editing ? t("editProtectedTarget") : t("addProtectedTarget")} description={t("protectedTargetDialogSub")} onClose={onClose} footer={<><Button onClick={onClose}>{t("cancel")}</Button><Button variant="primary" onClick={() => save.mutate()} disabled={!form.id || !form.hosts.some(Boolean) || !form.profile_id}>{t("save")}</Button></>}><div className="form-stack"><Field label={t("targetId")}><Input value={form.id} disabled={editing} onChange={event => setForm({ ...form, id: event.target.value })}/></Field><Field label={t("targetHosts")}><Input value={form.hosts.join(", ")} placeholder="api.openai.com" onChange={event => setForm({ ...form, hosts: event.target.value.split(",") })}/></Field><Field label={t("targetPort")}><Input type="number" min={1} max={65535} value={form.port || ""} onChange={event => setForm({ ...form, port: Number(event.target.value) || undefined })}/></Field><Field label={t("protocolProfile")}><Select value={form.profile_id} onValueChange={profile_id => setForm({ ...form, profile_id })} options={data.profiles.map(value => ({ value: value.id, label: value.name }))}/></Field><label className="field field--switch"><span>{t("enableProtection")}</span><Switch ariaLabel={t("enableProtection")} checked={form.enabled} onCheckedChange={enabled => setForm({ ...form, enabled })}/></label></div></Dialog>;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }
function certificateStatusDescription(status: SystemCertificateStatus | null, t: (key: MessageKey) => string) { if (!status) return t("certificateChecking"); if (!status.supported) return t("certificateUnsupported"); return status.installed ? t("certificateInstalledSub") : t("certificateNotInstalledSub"); }

const clientProxyRules: Record<AIClient, ProxyRule[]> = {
  claude: [{ id: "anthropic-api", hosts: ["api.anthropic.com"], port: 443, profile_id: "anthropic", enabled: true }],
  codex: [
    { id: "openai-api", hosts: ["api.openai.com"], port: 443, profile_id: "openai", enabled: true },
    { id: "chatgpt", hosts: ["chatgpt.com"], port: 443, profile_id: "codex-chatgpt", enabled: true },
  ],
};

async function ensureClientProxyRules(client: AIClient, configured: ProxyRule[]) {
  const pending = [...configured];
  for (const required of clientProxyRules[client]) {
    const existing = pending.find(item => item.hosts.some(host => required.hosts.includes(host)));
    if (existing) {
      if (!existing.enabled) {
        await coreApi.putProxyRule({ ...existing, enabled: true }, true);
        existing.enabled = true;
      }
      continue;
    }
    const id = availableId(required.id, pending.map(item => item.id));
    const item = { ...required, id, hosts: [...required.hosts] };
    await coreApi.putProxyRule(item, false);
    pending.push(item);
  }
}

function availableId(preferred: string, usedValues: string[]) { const used = new Set(usedValues); if (!used.has(preferred)) return preferred; let suffix = 2; while (used.has(`${preferred}-${suffix}`)) suffix += 1; return `${preferred}-${suffix}`; }
