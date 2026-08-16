import { Activity, FileClock, FlaskConical, Gauge, ListChecks, LockKeyhole, Network, RefreshCw, Settings, ShieldCheck } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
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
import { Switch } from "../shared/ui/Switch";

const icons = { overview: Gauge, logs: FileClock, test: FlaskConical, gateway: Network, models: Activity, rules: ListChecks, settings: Settings };
type View = keyof typeof icons | "settings";
const nav: View[] = ["overview", "logs", "test", "rules", "gateway", "models"];
function viewFromPath(pathname: string): View {
  const value = pathname.split("/")[1] as View | undefined;
  return value && (value in icons || value === "settings") ? value : "overview";
}
export function Shell() {
  const { t } = useI18n(); const { toast, notify } = useApp(); const queryClient = useQueryClient();
  const location = useLocation(); const navigate = useNavigate(); const view = viewFromPath(location.pathname);
  const core = useCore(); const connected = core.status === "online";
  const protectedByCore = connected && Boolean(core.policy.data?.enabled);
  const [restarting, setRestarting] = useState(false);
  const meta: Record<View, [string,string]> = { overview:[t("overviewTitle"),t("overviewSub")], logs:[t("logsTitle"),t("logsSub")], test:[t("testTitle"),t("testSub")], gateway:[t("gatewayTitle"),t("gatewaySub")], models:[t("modelsTitle"),t("modelsSub")], rules:[t("rulesTitle"),t("rulesSub")], settings:[t("settingsTitle"),t("settingsSub")] };
  const protection=useMutation({mutationFn:(enabled:boolean)=>coreApi.savePolicy({...core.policy.data!,enabled}),onSuccess:saved=>queryClient.setQueryData(queryKeys.policy,saved),onError:error=>notify(String(error))});
  const startProtection = useMutation({
    mutationFn: async () => {
      if (!("__TAURI_INTERNALS__" in window)) throw new Error(t("desktopOnly"));
      core.markStarting();
      await invoke("start_core", coreAddresses());
      const version = await waitForCore();
      queryClient.setQueryData(queryKeys.version, version);
      const currentPolicy = await coreApi.policy();
      const policy = currentPolicy.enabled ? currentPolicy : await coreApi.savePolicy({ ...currentPolicy, enabled: true });
      return { version, policy };
    },
    onSuccess: ({ version, policy }) => {
      queryClient.setQueryData(queryKeys.version, version);
      queryClient.setQueryData(queryKeys.policy, policy);
      void queryClient.invalidateQueries({ queryKey: queryKeys.root });
      notify(t("coreStarted"));
    },
    onError: error => { core.markOffline(); notify(String(error)); },
  });
  const coreTransitioning = core.status === "starting" || startProtection.isPending || restarting || (connected && core.policy.isFetching && core.policy.dataUpdatedAt === 0);
  const protectionLabel = coreTransitioning
    ? t("coreStarting")
    : protectedByCore ? t("protectionOn") : connected ? t("privacyProtectionPaused") : t("protectionUnavailable");
  useEffect(() => {
    if (core.status === "online") void queryClient.invalidateQueries({ queryKey: queryKeys.root });
  }, [core.status, queryClient]);
  function toggleProtection(enabled: boolean) {
    if (connected) protection.mutate(enabled);
    else if (enabled && core.status === "offline") startProtection.mutate();
  }
  async function restartCore() {
    if (restarting) return;
    if (!("__TAURI_INTERNALS__" in window)) {
      notify(t("desktopOnly"));
      return;
    }
    setRestarting(true);
    core.markStarting();
    try {
      await invoke("restart_core", coreAddresses());
      const version = await waitForCore();
      queryClient.setQueryData(queryKeys.version, version);
      await queryClient.invalidateQueries({ queryKey: queryKeys.root });
      notify(t("coreRestarted"));
    } catch (error) { core.markOffline(); notify(String(error)); }
    finally { setRestarting(false); }
  }
  async function startWindowDrag(event: MouseEvent<HTMLElement>) {
    if (event.button !== 0 || !("__TAURI_INTERNALS__" in window)) return;
    const target = event.target as HTMLElement;
    if (target.closest("button, input, select, textarea, [role='switch'], [data-no-drag]")) return;
    event.preventDefault();
    await getCurrentWindow().startDragging();
  }
  const page = <Suspense fallback={null}><Outlet/></Suspense>;
  return <div className="window-shell">
    <header className="titlebar" data-tauri-drag-region onMouseDown={startWindowDrag}><div className="brand" data-tauri-drag-region><strong data-tauri-drag-region>Remask</strong></div><div className="titlebar-drag" data-tauri-drag-region/><div className={`topbar-protection ${protectedByCore?"topbar-protection--active":""} ${coreTransitioning?"topbar-protection--starting":""}`}><ShieldCheck className="topbar-protection__icon" size={13}/><span>{protectionLabel}</span><Switch ariaLabel={t("privacyProtectionControl")} disabled={coreTransitioning||protection.isPending} checked={protectedByCore} onCheckedChange={toggleProtection}/></div></header>
    <div className="app-frame"><aside className="sidebar"><nav aria-label={t("overviewTitle")}>{nav.map((item) => { const Icon=icons[item]; const label=item==="rules"?t("rulesNav"):item==="test"?t("localTest"):item==="gateway"?t("gatewayNav"):t(item); return <button key={item} title={label} aria-label={label} aria-current={view===item?"page":undefined} className={`nav-item ${view===item?"nav-item--active":""}`} onClick={() => navigate(`/${item}`)}><Icon size={15}/><span>{label}</span></button>; })}</nav><div className="sidebar__bottom"><button title={t("settings")} aria-label={t("settings")} aria-current={view==="settings"?"page":undefined} className={`nav-item ${view==="settings"?"nav-item--active":""}`} onClick={()=>navigate("/settings")}><Settings size={15}/><span>{t("settings")}</span></button></div></aside>
      <main className={`${view==="overview"||view==="gateway"?"main--headerless":""} ${view==="models"?"main--models":""}`.trim()}>{view!=="overview"&&view!=="gateway"&&<header className="page-header"><div><h1>{meta[view][0]}</h1><p>{meta[view][1]}</p></div>{view==="test"?<span className="local-only-badge"><LockKeyhole size={11}/>{t("localOnly")}</span>:null}{view==="settings"&&<div className="page-header__actions"><Button variant="secondary" disabled={restarting} icon={<RefreshCw size={13}/>} onClick={restartCore}>{t(restarting ? "restarting" : "restart")}</Button></div>}</header>}<div className="page-content">{page}</div></main></div>
    <footer className="statusbar"><span className={core.status === "starting" ? "core-status--starting" : undefined}><StatusDot tone={connected?"success":core.status==="starting"?"warning":"muted"}/>{core.status==="starting"?t("coreStarting"):connected?t("coreOnline"):t("coreOffline")}</span><span className="spacer"/><span><code>remask-core</code> {connected?core.version.data?.version||"—":"—"}</span></footer><Toast message={toast}/>
  </div>;
}

function coreAddresses() {
  return {
    address: new URL(connection.core()).host,
    proxyAddress: new URL(connection.proxy()).host,
    forwardProxyAddress: new URL(connection.forwardProxy()).host,
  };
}

async function waitForCore(timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try { return await coreApi.version(); }
    catch (error) { lastError = error; }
    await new Promise(resolve => window.setTimeout(resolve, 350));
  }
  throw lastError ?? new Error("Core startup timed out");
}
