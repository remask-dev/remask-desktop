import { useQueryClient } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { open } from "@tauri-apps/plugin-dialog";
import { useEffect, useRef } from "react";
import { useApp } from "../../app/AppContext";
import { queryKeys } from "../../app/useCore";
import { connection, coreApi } from "../../shared/api/client";
import { useI18n } from "../../shared/i18n/I18n";

type QuickLaunchPreset = "claude-code" | "opencode" | "codex" | "codex-cli" | "terminal" | "browser";
type TrayLaunch = QuickLaunchPreset | "other";

/** Bridge native tray actions to the same protected-launch behavior as the UI. */
export function TrayActions() {
  const { notify } = useApp();
  const { locale, t } = useI18n();
  const queryClient = useQueryClient();
  const notifyRef = useRef(notify);

  useEffect(() => { notifyRef.current = notify; }, [notify]);

  useEffect(() => {
    if (!("__TAURI_INTERNALS__" in window)) return;
    let disposed = false;
    void invoke("set_tray_locale", { locale }).catch(error => notifyRef.current(String(error)));

    async function ensureProtection() {
      const policy = await coreApi.policy();
      const enabledPolicy = policy.enabled ? policy : await coreApi.updatePolicy({ enabled: true });
      if (!disposed) queryClient.setQueryData(queryKeys.policy, enabledPolicy);
    }

    async function launch(preset: TrayLaunch) {
      try {
        if (preset === "other") {
          const appPath = await open({ multiple: false, directory: false, title: t("selectApplication") });
          if (!appPath) return;
          await ensureProtection();
          await invoke("launch_app_with_proxy", { appPath, forwardProxyAddress: new URL(connection.forwardProxy()).host });
        } else {
          await ensureProtection();
          await invoke("launch_preset_with_proxy", { preset, forwardProxyAddress: new URL(connection.forwardProxy()).host });
        }
        if (!disposed) notifyRef.current(t("quickLaunchStarted"));
      } catch (error) {
        if (!disposed) notifyRef.current(String(error));
      }
    }

    const listeners = Promise.all([
      listen<string>("tray-safe-launch", event => { void launch(event.payload as TrayLaunch); }),
    ]);
    return () => {
      disposed = true;
      void listeners.then(unlisten => unlisten.forEach(stop => stop()));
    };
  }, [locale, queryClient, t]);

  return null;
}
