import { useMutation, useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { open } from "@tauri-apps/plugin-dialog";
import { AppWindow, Bot, ChevronDown, FolderOpen, Globe2, Rocket, SquareTerminal, Terminal } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useApp } from "../../app/AppContext";
import { queryKeys } from "../../app/useCore";
import { connection, coreApi } from "../../shared/api/client";
import { useI18n } from "../../shared/i18n/I18n";
import type { MessageKey } from "../../shared/i18n/messages";

export type QuickLaunchPreset = "claude-code" | "codex" | "codex-cli" | "terminal" | "browser";

const launchOptions: Array<{ value: QuickLaunchPreset; label: MessageKey; icon: ReactNode }> = [
  { value: "claude-code", label: "claudeCode", icon: <Bot size={15}/> },
  { value: "codex", label: "codexApp", icon: <AppWindow size={15}/> },
  { value: "codex-cli", label: "codexCLI", icon: <Terminal size={15}/> },
  { value: "browser", label: "browserApp", icon: <Globe2 size={15}/> },
  { value: "terminal", label: "terminalApp", icon: <SquareTerminal size={15}/> },
];

export function TopbarQuickLaunch() {
  const { t } = useI18n();
  const { notify } = useApp();
  const queryClient = useQueryClient();
  const [openMenu, setOpenMenu] = useState(false);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!openMenu) return;
    const closeOutside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpenMenu(false); };
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === "Escape") setOpenMenu(false); };
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [openMenu]);

  async function ensureProtection() {
    const policy = await coreApi.policy();
    if (policy.enabled) return;
    const saved = await coreApi.updatePolicy({ enabled: true });
    queryClient.setQueryData(queryKeys.policy, saved);
  }

  const launchPreset = useMutation({
    mutationFn: async (preset: QuickLaunchPreset) => {
      if (!("__TAURI_INTERNALS__" in window)) throw new Error(t("desktopAppRequired"));
      await ensureProtection();
      await invoke("launch_preset_with_proxy", { preset, forwardProxyAddress: new URL(connection.forwardProxy()).host });
    },
    onSuccess: () => notify(t("quickLaunchStarted")),
    onError: error => notify(String(error)),
  });

  const launchOther = useMutation({
    mutationFn: async () => {
      if (!("__TAURI_INTERNALS__" in window)) throw new Error(t("desktopAppRequired"));
      const appPath = await open({ multiple: false, directory: false, title: t("selectApplication") });
      if (!appPath) return false;
      await ensureProtection();
      await invoke("launch_app_with_proxy", { appPath, forwardProxyAddress: new URL(connection.forwardProxy()).host });
      return true;
    },
    onSuccess: launched => { if (launched) notify(t("appLaunched")); },
    onError: error => notify(String(error)),
  });

  const pending = launchPreset.isPending || launchOther.isPending;
  function selectPreset(preset: QuickLaunchPreset) {
    setOpenMenu(false);
    launchPreset.mutate(preset);
  }
  function selectOther() {
    setOpenMenu(false);
    launchOther.mutate();
  }

  return <div className="topbar-quick-launch" ref={root} data-no-drag>
    <button className="topbar-quick-launch__trigger" aria-haspopup="menu" aria-expanded={openMenu} disabled={pending} onClick={() => setOpenMenu(value => !value)}>
      <Rocket size={14}/><span>{t("quickLaunchAction")}</span><ChevronDown size={13}/>
    </button>
    {openMenu && <div className="topbar-quick-launch__menu" role="menu">
      {launchOptions.map(option => <button key={option.value} role="menuitem" onClick={() => selectPreset(option.value)}>{option.icon}<span>{t(option.label)}</span></button>)}
      <div className="topbar-quick-launch__divider"/>
      <button role="menuitem" onClick={selectOther}><FolderOpen size={15}/><span>{t("chooseAnotherApp")}</span></button>
    </div>}
  </div>;
}
