import { Activity, Bot, FileClock, FlaskConical, Gauge, ListChecks, LoaderCircle, LockKeyhole, RefreshCw, Settings, ShieldCheck } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { getCurrentWindow } from "@tauri-apps/api/window";
import { Suspense, useEffect, useState, type MouseEvent } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { connection, coreApi } from "../shared/api/client";
import { useI18n } from "../shared/i18n/I18n";
import { Button } from "../shared/ui/Button";
import { StatusDot } from "../shared/ui/Status";
import { Toast } from "../shared/ui/Toast";
import { useApp } from "./AppContext";
import { queryKeys, useCore } from "./useCore";
import { PageState } from "../shared/ui/PageState";
import { Switch } from "../shared/ui/Switch";
import { useMutation } from "@tanstack/react-query";

const icons = { overview: Gauge, logs: FileClock, test: FlaskConical, services: Bot, models: Activity, rules: ListChecks, settings: Settings };
type View = keyof typeof icons | "settings";
const nav: View[] = ["overview", "logs", "test", "rules", "services", "models"];
function viewFromPath(pathname: string): View {
  const value = pathname.split("/")[1] as View | undefined;
  return value && (value in icons || value === "settings") ? value : "overview";
}
export function Shell() {
  const { t } = useI18n(); const { toast, notify } = useApp(); const queryClient = useQueryClient();
  const location = useLocation(); const navigate = useNavigate(); const view = viewFromPath(location.pathname);
  const core = useCore(); const connected = Boolean(core.version.data);
  const initialLoading = core.isPending && !core.version.data;
  const [restarting, setRestarting] = useState(false);
  const meta: Record<View, [string,string]> = { overview:[t("overviewTitle"),t("overviewSub")], logs:[t("logsTitle"),t("logsSub")], test:[t("testTitle"),t("testSub")], services:[t("servicesTitle"),t("servicesSub")], models:[t("modelsTitle"),t("modelsSub")], rules:[t("rulesTitle"),t("rulesSub")], settings:[t("settingsTitle"),t("settingsSub")] };
  const protection=useMutation({mutationFn:(enabled:boolean)=>coreApi.savePolicy({...core.policy.data!,enabled}),onSuccess:saved=>queryClient.setQueryData(queryKeys.policy,saved),onError:error=>notify(String(error))});
  async function restartCore() {
    if (restarting) return;
    if (!("__TAURI_INTERNALS__" in window)) {
      notify(t("desktopOnly"));
      return;
    }
    setRestarting(true);
    try {
      await invoke("restart_core", { address: new URL(connection.core()).host, proxyAddress: new URL(connection.proxy()).host, forwardProxyAddress: new URL(connection.forwardProxy()).host });
      await new Promise(resolve => window.setTimeout(resolve, 600));
      await queryClient.invalidateQueries({ queryKey: queryKeys.root }).catch(() => undefined);
      notify(t("coreRestarted"));
    } catch (error) { notify(String(error)); }
    finally { setRestarting(false); }
  }
  async function startWindowDrag(event: MouseEvent<HTMLElement>) {
    if (event.button !== 0 || !("__TAURI_INTERNALS__" in window)) return;
    const target = event.target as HTMLElement;
    if (target.closest("button, input, select, textarea, [role='switch'], [data-no-drag]")) return;
    event.preventDefault();
    await getCurrentWindow().startDragging();
  }
  const page = initialLoading ? null : !core.version.data ? <Disconnected restart={restartCore}/> : <Suspense fallback={<PageState pending/>}><Outlet/></Suspense>;
  return <div className="window-shell">
    <header className="titlebar" data-tauri-drag-region onMouseDown={startWindowDrag}><div className="brand" data-tauri-drag-region><strong data-tauri-drag-region>Remask</strong></div><div className="titlebar-drag" data-tauri-drag-region/>{!initialLoading&&core.policy.data&&<div className={`topbar-protection ${core.policy.data.enabled?"topbar-protection--active":""}`}><ShieldCheck className="topbar-protection__icon" size={13}/><span>{core.policy.data.enabled?t("protectionOn"):t("protectionOff")}</span><Switch ariaLabel={t("globalProtection")} disabled={!connected||protection.isPending} checked={core.policy.data.enabled} onCheckedChange={enabled=>protection.mutate(enabled)}/></div>}</header>
    <div className="app-frame"><aside className="sidebar"><nav>{nav.map((item) => { const Icon=icons[item]; const label=item==="rules"?t("rulesNav"):item==="test"?t("localTest"):t(item); return <button key={item} title={label} aria-label={label} className={`nav-item ${view===item?"nav-item--active":""}`} onClick={() => navigate(`/${item}`)}><Icon size={15}/><span>{label}</span></button>; })}</nav><div className="sidebar__bottom"><button title={t("settings")} aria-label={t("settings")} className={`nav-item ${view==="settings"?"nav-item--active":""}`} onClick={()=>navigate("/settings")}><Settings size={15}/><span>{t("settings")}</span></button></div></aside>
      <main className={view==="overview"?"main--headerless":""}>{view!=="overview"&&<header className="page-header"><div><h1>{meta[view][0]}</h1><p>{meta[view][1]}</p></div>{view==="test"?<span className="local-only-badge"><LockKeyhole size={11}/>{t("localOnly")}</span>:null}{view==="settings"&&<div className="page-header__actions"><Button variant="secondary" disabled={restarting} icon={<RefreshCw size={13}/>} onClick={restartCore}>{t(restarting ? "restarting" : "restart")}</Button></div>}</header>}<div className="page-content">{page}</div></main></div>
    <footer className="statusbar">{!initialLoading&&<><span><StatusDot tone={connected?"success":"muted"}/>{connected?t("coreOnline"):t("coreOffline")}</span><span className="spacer"/><span><code>remask-core</code> {core.version.data?.version||"—"}</span></>}</footer><Toast message={toast}/>
  </div>;
}
function Disconnected({restart}:{restart:()=>void}) {
  const {t}=useI18n();
  const [showRestart, setShowRestart] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setShowRestart(true), 15_000);
    return () => window.clearTimeout(timer);
  }, []);

  return <div className="empty-page" aria-busy="true"><LoaderCircle className="core-loading-indicator" size={28} strokeWidth={1.5}/>{showRestart&&<Button className="mt-4" variant="secondary" onClick={restart} icon={<RefreshCw size={14}/>}>{t("restart")}</Button>}</div>;
}
