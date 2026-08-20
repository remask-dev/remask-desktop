import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { Copy, FolderOpen, Network, Plus, Server, ShieldCheck } from "lucide-react";
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
const blankProxyRule: ProxyRule = { id: "", hosts: [""], profile_id: "generic", enabled: true };
type GatewayData = { upstreams: Upstream[]; profiles: Profile[]; proxyCA: ProxyCAStatus; proxyRules: ProxyRule[] };
type SystemCertificateStatus = { supported: boolean; installed: boolean; platform: string };

export function Gateway() {
  const query = useGatewayData();
  if (query.isPending || !query.upstreams || !query.profiles || !query.proxyCA || !query.proxyRules) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <GatewayContent data={{ upstreams: query.upstreams, profiles: query.profiles, proxyCA: query.proxyCA, proxyRules: query.proxyRules }}/>;
}

function GatewayContent({ data }: { data: GatewayData }) {
  const { t } = useI18n();
  const [tab, setTab] = useState<"api" | "proxy">("proxy");
  return <div className="gateway-page">
    <header className="page-header gateway-header">
      <div className="gateway-header__title"><h1>{t("gatewayTitle")}</h1><p>{t(tab === "api" ? "apiGatewayDescription" : "httpProxyDescription")}</p></div>
      <div className="gateway-tabs" role="tablist" aria-label={t("gatewayTitle")}>
        <button id="gateway-tab-api" role="tab" aria-controls="gateway-panel" aria-selected={tab === "api"} className={tab === "api" ? "active" : ""} onClick={() => setTab("api")}><Server size={15}/><strong>{t("apiGateway")}</strong></button>
        <button id="gateway-tab-proxy" role="tab" aria-controls="gateway-panel" aria-selected={tab === "proxy"} className={tab === "proxy" ? "active" : ""} onClick={() => setTab("proxy")}><Network size={15}/><strong>{t("proxyGateway")}</strong></button>
      </div>
    </header>
    <div id="gateway-panel" className="gateway-stage" role="tabpanel" aria-labelledby={tab === "api" ? "gateway-tab-api" : "gateway-tab-proxy"}>{tab === "api" ? <APIGateway data={data}/> : <ProxyGateway data={data}/>}</div>
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
    <GatewayAccessBar icon={<Server size={14}/>} title={t("apiGateway")} value={connection.proxy()} onCopy={copy}/>
    <div className="split-view gateway-split"><section className="list-pane"><div className="pane-title"><div><span>{t("providers")}</span><small>{data.upstreams.length}</small></div><Button variant="primary" icon={<Plus size={13}/>} onClick={() => setEditing({ ...blankProvider })}>{t("addProvider")}</Button></div><div className="service-list gateway-entity-list">{data.upstreams.map(item => <div key={item.id} className={`gateway-entity-row ${current?.id === item.id ? "selected" : ""} ${item.enabled ? "" : "disabled"}`}><button className="gateway-entity-select" onClick={() => setSelected(item.id)}><span className="service-icon"><Server size={15}/></span><span><strong>{item.id}</strong><code>{item.base_url}</code><small>{item.profile_id}</small></span></button><Switch ariaLabel={`${item.id} ${t("enabled")}`} disabled={toggle.isPending} checked={item.enabled} onCheckedChange={() => toggle.mutate(item)}/></div>)}</div></section><section className="detail-pane">{current ? <><header className="detail-header"><div><span>{t("provider")}</span><h2>{current.id}</h2><code>{current.base_url}</code></div><div className="header-actions"><Button onClick={() => setEditing({ ...current })}>{t("edit")}</Button><Button variant="danger" onClick={() => setConfirm(current.id)}>{t("remove")}</Button></div></header><section className="detail-section gateway-options"><h3>{t("apiAccessAddress")}</h3><GatewayAddress label={t("sdkBaseUrl")} value={`${connection.proxy()}/proxy/${current.id}`} onCopy={copy}/><p>{t("apiAccessNote")}</p></section><section className="detail-section detail-section--summary"><h3>{t("connectionConfig")}</h3><dl className="property-grid"><div><dt>{t("profile")}</dt><dd title={current.profile_id}>{current.profile_id}</dd></div><div><dt>{t("credential")}</dt><dd title={current.credential_mode === "managed" ? t("managed") : t("pass")}>{current.credential_mode === "managed" ? t("managed") : t("pass")}</dd></div></dl></section></> : <GatewayEmpty icon={<Server size={24}/>} title={t("addProvider")} description={t("providerEmpty")}/>}</section></div>
    <ProviderDialog item={editing} data={data} onClose={() => setEditing(null)}/>
    <Dialog open={!!confirm} title={t("confirmDeleteProvider")} onClose={() => setConfirm(null)} footer={<><Button onClick={() => setConfirm(null)}>{t("cancel")}</Button><Button variant="danger" onClick={() => confirm && remove.mutate(confirm)}>{t("confirm")}</Button></>}><p className="dialog-message">{confirm}</p></Dialog>
  </div>;
}

function ProxyGateway({ data }: { data: GatewayData }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient();
  const [selected, setSelected] = useState(data.proxyRules[0]?.id || "");
  const [editing, setEditing] = useState<ProxyRule | null>(null);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [certificateStatus, setCertificateStatus] = useState<SystemCertificateStatus | null>(() => inTauri ? null : { supported: false, installed: false, platform: "browser" });
  const [confirmCertificateInstall, setConfirmCertificateInstall] = useState(false);
  const [confirmCertificateUninstall, setConfirmCertificateUninstall] = useState(false);
  const current = data.proxyRules.find(item => item.id === selected) || data.proxyRules[0];
  useEffect(() => { if (!inTauri) return; invoke<SystemCertificateStatus>("system_certificate_status").then(setCertificateStatus).catch(() => setCertificateStatus({ supported: false, installed: false, platform: "unknown" })); }, []);
  const remove = useMutation({ mutationFn: coreApi.deleteProxyRule, onSuccess: () => { setConfirm(null); setSelected(""); void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }); }, onError: error => notify(String(error)) });
  const toggle = useMutation({ mutationFn: (item: ProxyRule) => coreApi.putProxyRule({ ...item, enabled: !item.enabled }, true), onSuccess: () => void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }), onError: error => notify(String(error)) });
  const installCertificate = useMutation({ mutationFn: () => invoke<SystemCertificateStatus>("install_system_certificate"), onSuccess: status => { setCertificateStatus(status); setConfirmCertificateInstall(false); notify(t("certificateInstalled")); }, onError: error => notify(String(error)) });
  const uninstallCertificate = useMutation({ mutationFn: () => invoke<SystemCertificateStatus>("uninstall_system_certificate"), onSuccess: status => { setCertificateStatus(status); setConfirmCertificateUninstall(false); notify(t("certificateUninstalled")); }, onError: error => notify(String(error)) });
  async function copy(value: string) { await navigator.clipboard.writeText(value); notify(t("copied")); }
  const env = proxyEnvironment(connection.forwardProxy(), connection.socksProxy(), data.proxyCA.certificate_path);
  return <div className="gateway-workspace">
    <section className="proxy-access-bar">
      <div className="proxy-access-primary">
        <GatewayAccessAddress icon={<Network size={14}/>} title={t("httpProxyAddress")} value={connection.forwardProxy()} onCopy={copy}/>
        <GatewayAccessAddress title={t("socksProxyAddress")} value={connection.socksProxy()} onCopy={copy}/>
        <span className="proxy-access-ca" title={certificateStatusDescription(certificateStatus, t)}><span>{t("systemCertificate")}</span><CertificateStatusAction certificateStatus={certificateStatus} actionDisabled={!data.proxyCA.certificate_path} onInstall={() => setConfirmCertificateInstall(true)} onUninstall={() => setConfirmCertificateUninstall(true)} showAction={false}/>{inTauri && certificateStatus?.supported && <CertificateStatusAction certificateStatus={certificateStatus} actionDisabled={!data.proxyCA.certificate_path} onInstall={() => setConfirmCertificateInstall(true)} onUninstall={() => setConfirmCertificateUninstall(true)}/>}</span>
      </div>
    </section>
      <div className="split-view gateway-split"><section className="list-pane"><div className="pane-title"><div><span>{t("protectedTargets")}</span><small>{data.proxyRules.length}</small></div><Button variant="primary" icon={<Plus size={13}/>} onClick={() => setEditing({ ...blankProxyRule, hosts: [...blankProxyRule.hosts] })}>{t("addProtectedTarget")}</Button></div><div className="service-list gateway-entity-list">{data.proxyRules.map(item => <div key={item.id} className={`gateway-entity-row ${current?.id === item.id ? "selected" : ""} ${item.enabled ? "" : "disabled"}`}><button className="gateway-entity-select" onClick={() => setSelected(item.id)}><span className="service-icon"><ShieldCheck size={15}/></span><span><strong>{item.id}</strong><code>{item.hosts[0] || "—"}</code><small>{item.profile_id}</small></span></button><Switch ariaLabel={`${item.id} ${t("enableProtection")}`} disabled={toggle.isPending} checked={item.enabled} onCheckedChange={() => toggle.mutate(item)}/></div>)}</div></section><section className="detail-pane">{current ? <><header className="detail-header"><div><span>{t("protectedTarget")}</span><h2>{current.id}</h2></div><div className="header-actions"><Button onClick={() => setEditing({ ...current, hosts: [...current.hosts] })}>{t("edit")}</Button><Button variant="danger" onClick={() => setConfirm(current.id)}>{t("remove")}</Button></div></header><section className="detail-section detail-section--summary"><h3>{t("matchingScope")}</h3><div className="gateway-hosts"><span>{t("targetHost")}</span><div>{current.hosts.map((host, index) => <code key={`${host}-${index}`} title={host}>{host}</code>)}</div></div><dl className="property-grid"><div><dt>{t("profile")}</dt><dd title={current.profile_id}>{current.profile_id}</dd></div><div><dt>{t("tlsInspection")}</dt><dd className={current.enabled ? "success" : "warning"} title={current.enabled ? t("enabled") : t("disabledStatus")}>{current.enabled ? t("enabled") : t("disabledStatus")}</dd></div></dl></section><ProxyConnectionDetails env={env} proxyCA={data.proxyCA} certificateStatus={certificateStatus} certificateActionPending={installCertificate.isPending || uninstallCertificate.isPending} onCopy={copy} onInstall={() => setConfirmCertificateInstall(true)} onUninstall={() => setConfirmCertificateUninstall(true)}/></> : <GatewayEmpty icon={<ShieldCheck size={24}/>} title={t("addProtectedTarget")} description={t("protectedTargetEmpty")}/>}</section></div>
    <ProxyRuleDialog item={editing} data={data} onClose={() => setEditing(null)}/>
    <Dialog open={!!confirm} title={t("confirmDeleteTarget")} onClose={() => setConfirm(null)} footer={<><Button onClick={() => setConfirm(null)}>{t("cancel")}</Button><Button variant="danger" onClick={() => confirm && remove.mutate(confirm)}>{t("confirm")}</Button></>}><p className="dialog-message">{confirm}</p></Dialog>
    <Dialog open={confirmCertificateInstall} title={t("certificateInstallConfirm")} onClose={() => setConfirmCertificateInstall(false)} footer={<><Button onClick={() => setConfirmCertificateInstall(false)}>{t("cancel")}</Button><Button variant="primary" disabled={installCertificate.isPending} onClick={() => installCertificate.mutate()}>{t("installCertificate")}</Button></>}><p className="dialog-message">{t("certificateInstallDescription")}</p>{data.proxyCA.fingerprint_sha256 && <code className="certificate-fingerprint">SHA-256 {data.proxyCA.fingerprint_sha256}</code>}</Dialog>
    <Dialog open={confirmCertificateUninstall} title={t("certificateUninstallConfirm")} onClose={() => setConfirmCertificateUninstall(false)} footer={<><Button onClick={() => setConfirmCertificateUninstall(false)}>{t("cancel")}</Button><Button variant="danger" disabled={uninstallCertificate.isPending} onClick={() => uninstallCertificate.mutate()}>{t("uninstallCertificate")}</Button></>}><p className="dialog-message">{t("certificateUninstallDescription")}</p>{data.proxyCA.fingerprint_sha256 && <code className="certificate-fingerprint">SHA-256 {data.proxyCA.fingerprint_sha256}</code>}</Dialog>
  </div>;
}

function GatewayAccessBar({ icon, title, value, onCopy }: { icon: React.ReactNode; title: string; value: string; onCopy: (value: string) => void }) {
  return <section className="proxy-access-bar"><GatewayAccessAddress icon={icon} title={title} value={value} onCopy={onCopy}/></section>;
}
function GatewayAccessAddress({ icon, title, value, onCopy }: { icon?: React.ReactNode; title: string; value: string; onCopy: (value: string) => void }) {
  return <div className="proxy-access-address">{icon && <span className="proxy-access-icon">{icon}</span>}<span><small>{title}</small><button onClick={() => void onCopy(value)}><code>{value}</code><Copy size={12}/></button></span></div>;
}
function ProxyConnectionDetails({ env, proxyCA, certificateStatus, certificateActionPending, onCopy, onInstall, onUninstall }: { env: string; proxyCA: ProxyCAStatus; certificateStatus: SystemCertificateStatus | null; certificateActionPending: boolean; onCopy: (value: string) => void; onInstall: () => void; onUninstall: () => void }) {
  const { t } = useI18n();
  return <section className="detail-section proxy-connection"><h3>{t("systemIntegration")}</h3><div className="proxy-connection__certificate"><header><div><strong>{t("systemCertificate")}</strong><small>{certificateStatusDescription(certificateStatus, t)}</small></div><div><CertificateStatusAction certificateStatus={certificateStatus} actionDisabled={!proxyCA.certificate_path || certificateActionPending} onInstall={onInstall} onUninstall={onUninstall}/></div></header>{proxyCA.certificate_path && <button className="proxy-connection__path" onClick={() => void onCopy(proxyCA.certificate_path!)}><code>{proxyCA.certificate_path}</code><Copy size={12}/></button>}</div><div className="proxy-connection__environment"><header><div><strong>{t("copyProxyEnv")}</strong><small>{t("proxyEnvironmentNoBypassSub")}</small></div></header><button className="proxy-connection__env" onClick={() => void onCopy(env)}><code>{env}</code><Copy size={13}/></button></div></section>;
}
function CertificateStatusAction({ certificateStatus, actionDisabled, onInstall, onUninstall, showAction = true }: { certificateStatus: SystemCertificateStatus | null; actionDisabled: boolean; onInstall: () => void; onUninstall: () => void; showAction?: boolean }) {
  const { t } = useI18n();
  if (!certificateStatus) return <Badge tone="neutral">{t("certificateChecking")}</Badge>;
  if (showAction && inTauri && certificateStatus.supported) return certificateStatus.installed
    ? <Button variant="danger" className="w-auto min-w-[76px] px-2.5" disabled={actionDisabled} onClick={onUninstall}>{t("uninstallCertificate")}</Button>
    : <Button className="w-auto min-w-[76px] px-2.5" disabled={actionDisabled} onClick={onInstall}>{t("installCertificate")}</Button>;
  if (certificateStatus.installed) return <Badge tone="success">{t("certificateInstalledStatus")}</Badge>;
  return <Badge tone="warning">{t("certificateNotInstalledStatus")}</Badge>;
}
function GatewayAddress({ label, value, onCopy }: { label: string; value: string; onCopy: (value: string) => void }) { return <button className="copy-field copy-field--labeled" onClick={() => onCopy(value)}><span><small>{label}</small><code>{value}</code></span><Copy size={14}/></button>; }
function GatewayEmpty({ icon, title, description }: { icon: React.ReactNode; title: string; description: string }) { return <div className="detail-empty"><span className="detail-empty__icon">{icon}</span><h2>{title}</h2><p>{description}</p></div>; }

function ProviderDialog({ item, data, onClose }: { item: Upstream | null; data: GatewayData; onClose: () => void }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient(); const editing = !!item && data.upstreams.some(value => value.id === item.id); const [form, setForm] = useState<Upstream>(item || blankProvider);
  useEffect(() => { setForm(item ? { ...item } : { ...blankProvider }); }, [item]);
  const profile = data.profiles.find(value => value.id === form.profile_id);
  const save = useMutation({ mutationFn: () => coreApi.putUpstream(form, editing), onSuccess: () => { void qc.invalidateQueries({ queryKey: queryKeys.upstreams }); onClose(); }, onError: error => notify(String(error)) });
  if (!item) return null;
  const missingManagedAPIKey = form.credential_mode === "managed" && !editing && !form.api_key?.trim();
  return <Dialog open title={editing ? t("editProvider") : t("addProvider")} description={t("credentialSafety")} onClose={onClose} footer={<><ProfileDirectoryButton/><Button onClick={onClose}>{t("cancel")}</Button><Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !form.id.trim() || !form.base_url.trim() || !form.profile_id || missingManagedAPIKey}>{t("save")}</Button></>}><div className="form-stack"><Field label={t("providerId")}><Input value={form.id} disabled={editing} onChange={event => setForm({ ...form, id: event.target.value })}/></Field><Field label={t("upstreamUrl")}><Input type="url" value={form.base_url} onChange={event => setForm({ ...form, base_url: event.target.value })}/></Field><Field label={t("protocolProfile")}><Select value={form.profile_id} onValueChange={profile_id => setForm({ ...form, profile_id })} options={data.profiles.map(value => ({ value: value.id, label: value.name }))}/></Field><Field label={t("credential")}><Select value={form.credential_mode} onValueChange={value => setForm({ ...form, credential_mode: value as Upstream["credential_mode"], api_key: value === "managed" ? form.api_key : undefined })} options={[{ value: "passthrough", label: t("pass") }, { value: "managed", label: t("managed") }]}/></Field>{form.credential_mode === "managed" && <Field label={t("apiKey")}><Input type="password" value={form.api_key === "••••••••" ? "" : form.api_key || ""} onChange={event => setForm({ ...form, api_key: event.target.value })} placeholder={profile?.header_templates ? Object.values(profile.header_templates).find(value => value.includes("{{api_key}}")) || "sk-…" : "sk-…"}/></Field>}<label className="field field--switch"><span>{t("enableProvider")}</span><Switch ariaLabel={t("enableProvider")} checked={form.enabled} onCheckedChange={enabled => setForm({ ...form, enabled })}/></label></div></Dialog>;
}

function ProxyRuleDialog({ item, data, onClose }: { item: ProxyRule | null; data: GatewayData; onClose: () => void }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient(); const editing = !!item && data.proxyRules.some(value => value.id === item.id); const [form, setForm] = useState<ProxyRule>(item || blankProxyRule);
  useEffect(() => { setForm(item ? { ...item, hosts: [...item.hosts] } : { ...blankProxyRule, hosts: [...blankProxyRule.hosts] }); }, [item]);
  const save = useMutation({ mutationFn: () => coreApi.putProxyRule({ ...form, hosts: form.hosts.map(host => host.trim()).filter(Boolean) }, editing), onSuccess: () => { void qc.invalidateQueries({ queryKey: queryKeys.proxyRules }); onClose(); }, onError: error => notify(String(error)) });
  if (!item) return null;
  return <Dialog open title={editing ? t("editProtectedTarget") : t("addProtectedTarget")} description={t("protectedTargetDialogSub")} onClose={onClose} footer={<><ProfileDirectoryButton/><Button onClick={onClose}>{t("cancel")}</Button><Button variant="primary" onClick={() => save.mutate()} disabled={save.isPending || !form.id || !form.hosts.some(host => host.trim()) || !form.profile_id}>{t("save")}</Button></>}><div className="form-stack"><Field label={t("targetId")}><Input value={form.id} disabled={editing} onChange={event => setForm({ ...form, id: event.target.value })}/></Field><label className="field field--multiline"><span>{t("targetHosts")}</span><textarea rows={5} value={form.hosts.join("\n")} placeholder={"*.example.com\napi.openai.com:443\n"} onChange={event => setForm({ ...form, hosts: event.target.value.split(/\r?\n/) })}/></label><Field label={t("protocolProfile")}><Select value={form.profile_id} onValueChange={profile_id => setForm({ ...form, profile_id })} options={data.profiles.map(value => ({ value: value.id, label: value.name }))}/></Field><label className="field field--switch"><span>{t("enableProtection")}</span><Switch ariaLabel={t("enableProtection")} checked={form.enabled} onCheckedChange={enabled => setForm({ ...form, enabled })}/></label></div></Dialog>;
}

function ProfileDirectoryButton() {
  const { t } = useI18n(); const { notify } = useApp();
  async function openDirectory() {
    if (!inTauri) { notify(t("desktopOnly")); return; }
    try { await invoke("open_profile_directory"); }
    catch (error) { notify(String(error)); }
  }
  return <Button variant="ghost" className="mr-auto px-1.5" icon={<FolderOpen size={14}/>} onClick={() => void openDirectory()}>{t("openProfileConfig")}</Button>;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="field"><span>{label}</span>{children}</label>; }
function certificateStatusDescription(status: SystemCertificateStatus | null, t: (key: MessageKey) => string) { if (!status) return t("certificateChecking"); if (!status.supported) return t("certificateUnsupported"); return status.installed ? t("certificateInstalledSub") : t("certificateNotInstalledSub"); }

function proxyEnvironment(proxyUrl: string, socksProxyUrl: string, certificatePath?: string) {
  const proxyAssignments: Array<[string, string]> = [
    ["HTTP_PROXY", proxyUrl], ["HTTPS_PROXY", proxyUrl],
    ["http_proxy", proxyUrl], ["https_proxy", proxyUrl],
    ["ALL_PROXY", socksProxyUrl], ["all_proxy", socksProxyUrl],
  ];
  const certificateAssignments: Array<[string, string]> = certificatePath ? [
    ["NODE_EXTRA_CA_CERTS", certificatePath], ["SSL_CERT_FILE", certificatePath],
    ["REQUESTS_CA_BUNDLE", certificatePath], ["CURL_CA_BUNDLE", certificatePath],
  ] : [];
  return [proxyAssignments, certificateAssignments]
    .filter(assignments => assignments.length)
    .map(assignments => `export ${assignments.map(([key, value]) => `${key}=${shellQuote(value)}`).join(" ")}`)
    .join("\n");
}

function shellQuote(value: string) { return `'${value.replaceAll("'", `'"'"'`)}'`; }
