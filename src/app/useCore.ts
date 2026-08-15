import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useState } from "react";
import { coreApi } from "../shared/api/client";
import type { AuditSettings, AuditStats, AuditStatsRange, PolicySettings, RuntimeStatus } from "../shared/api/types";

/**
 * Query keys are the resource boundary for the desktop client. Mutations can
 * invalidate one resource (or the root) without causing unrelated pages to
 * fetch their data again.
 */
export const queryKeys = {
  root: ["core"] as const,
  version: ["core", "version"] as const,
  policy: ["core", "policy"] as const,
  proxyCA: ["core", "proxy-ca"] as const,
  profiles: ["core", "profiles"] as const,
  upstreams: ["core", "upstreams"] as const,
  models: ["core", "models"] as const,
  activeModel: ["core", "models", "active"] as const,
  settings: ["core", "settings"] as const,
  stats: (range: AuditStatsRange) => ["core", "audit-stats", range] as const,
  logs: (query = "") => ["core", "audit-logs", query] as const,
  log: (id: string) => ["core", "audit-log", id] as const,
};

export type CoreStatus = "starting" | "online" | "offline";

const emptyPolicy = (): PolicySettings => ({ enabled: false, redact_ai_answers: false, entity_types: [], rules: [] });
const emptyRuntime = (): RuntimeStatus => ({ available: false, name: "unavailable" });
const emptyStats = (): AuditStats => ({
  requests: 0, entities: 0, success_rate: 0, average_latency_ms: 0, streaming_requests: 0,
  entity_types: {}, daily: [], token_input: 0, token_output: 0, token_total: 0, token_cached: 0, tokens_per_minute: 0,
});
const emptySettings = (): AuditSettings => ({
  record_request_content: false, debug: false, retention_days: 30, max_inference_tokens: 512,
  inference_provider: "cpu", entity_cache_enabled: true, entity_cache_ttl_seconds: 300,
});
const emptyProxyCA = () => ({ ready: false, certificate_path: undefined, fingerprint_sha256: "" });

/** Subscribe to the shell's liveness query without starting another probe. */
function useCoreOnline() {
  const version = useQuery({ queryKey: queryKeys.version, queryFn: coreApi.version, enabled: false });
  return Boolean(version.data) && version.dataUpdatedAt > version.errorUpdatedAt;
}

/** Global shell data: cheap liveness/version plus the protection switch. */
export function useCore() {
  // The desktop process starts Core after the webview is shown. Keep that
  // transition explicit, but never use it to block rendering the app shell.
  const [starting, setStarting] = useState(() => "__TAURI_INTERNALS__" in window);
  const version = useQuery({
    queryKey: queryKeys.version,
    queryFn: coreApi.version,
    networkMode: "always",
    refetchInterval: query => starting
      ? 500
      : query.state.dataUpdatedAt > query.state.errorUpdatedAt ? 5_000 : 2_000,
    refetchIntervalInBackground: true,
    retry: false,
  });
  // A failed background refetch can retain old data. Compare update times so
  // a dead Core cannot continue to look online merely because it was once up.
  const online = Boolean(version.data) && version.dataUpdatedAt > version.errorUpdatedAt;
  const status: CoreStatus = online ? "online" : starting || version.isPending ? "starting" : "offline";
  const policy = useQuery({
    queryKey: queryKeys.policy,
    queryFn: coreApi.policy,
    enabled: online,
    networkMode: "always",
    initialData: emptyPolicy,
    refetchInterval: online ? 15_000 : false,
    retry: false,
  });

  useEffect(() => {
    if (online) setStarting(false);
  }, [online]);

  useEffect(() => {
    if (!starting) return;
    const timeout = window.setTimeout(() => setStarting(false), 20_000);
    return () => window.clearTimeout(timeout);
  }, [starting]);

  const markStarting = useCallback(() => setStarting(true), []);
  const markOffline = useCallback(() => setStarting(false), []);

  return {
    version,
    policy,
    status,
    markStarting,
    markOffline,
  };
}

export function useOverviewData(range: AuditStatsRange) {
  const online = useCoreOnline();
  const stats = useQuery({ queryKey: queryKeys.stats(range), queryFn: () => coreApi.stats(range), enabled: online, refetchInterval: online ? 15_000 : false, initialData: emptyStats, initialDataUpdatedAt: 0 });
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy, enabled: online, initialData: emptyPolicy, initialDataUpdatedAt: 0 });
  const activeModel = useQuery({ queryKey: queryKeys.activeModel, queryFn: coreApi.activeModel, enabled: online, initialData: null, initialDataUpdatedAt: 0 });
  return combineQueries({ stats, policy, activeModel });
}

export function useLogsData(query = "") {
  const online = useCoreOnline();
  return useQuery({ queryKey: queryKeys.logs(query), queryFn: () => coreApi.logs(query), enabled: online, initialData: [], initialDataUpdatedAt: 0 });
}

export function useLogDetailData(id: string) {
  const online = useCoreOnline();
  return useQuery({ queryKey: queryKeys.log(id), queryFn: () => coreApi.log(id), enabled: online && Boolean(id), staleTime: Infinity });
}

export function useServicesData() {
  const online = useCoreOnline();
  const upstreams = useQuery({ queryKey: queryKeys.upstreams, queryFn: coreApi.upstreams, enabled: online, initialData: [], initialDataUpdatedAt: 0 });
  const profiles = useQuery({ queryKey: queryKeys.profiles, queryFn: coreApi.profiles, enabled: online, initialData: [], initialDataUpdatedAt: 0 });
  const proxyCA = useQuery({ queryKey: queryKeys.proxyCA, queryFn: coreApi.proxyCA, enabled: online, initialData: emptyProxyCA, initialDataUpdatedAt: 0 });
  return combineQueries({ upstreams, profiles, proxyCA });
}

export function useModelsData() {
  const online = useCoreOnline();
  const models = useQuery({ queryKey: queryKeys.models, queryFn: coreApi.models, enabled: online, initialData: () => ({ models: [], runtime: emptyRuntime() }), initialDataUpdatedAt: 0 });
  const activeModel = useQuery({ queryKey: queryKeys.activeModel, queryFn: coreApi.activeModel, enabled: online, initialData: null, initialDataUpdatedAt: 0 });
  return combineQueries({ models, activeModel });
}

export function useRulesData() {
  const online = useCoreOnline();
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy, enabled: online, initialData: emptyPolicy, initialDataUpdatedAt: 0 });
  const models = useQuery({ queryKey: queryKeys.models, queryFn: coreApi.models, enabled: online, initialData: () => ({ models: [], runtime: emptyRuntime() }), initialDataUpdatedAt: 0 });
  return combineQueries({ policy, models });
}

export function useSettingsData() {
  const online = useCoreOnline();
  const settings = useQuery({ queryKey: queryKeys.settings, queryFn: coreApi.settings, enabled: online, initialData: emptySettings, initialDataUpdatedAt: 0 });
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy, enabled: online, initialData: emptyPolicy, initialDataUpdatedAt: 0 });
  const upstreams = useQuery({ queryKey: queryKeys.upstreams, queryFn: coreApi.upstreams, enabled: online, initialData: [], initialDataUpdatedAt: 0 });
  const proxyCA = useQuery({ queryKey: queryKeys.proxyCA, queryFn: coreApi.proxyCA, enabled: online, initialData: emptyProxyCA, initialDataUpdatedAt: 0 });
  return combineQueries({ settings, policy, upstreams, proxyCA });
}

type QueryResult = { isPending: boolean; error: unknown; isError: boolean; refetch: () => Promise<unknown> };
function combineQueries<T extends Record<string, QueryResult>>(queries: T) {
  const values = Object.values(queries);
  const isPending = values.some(query => query.isPending);
  const error = values.find(query => query.error)?.error;
  const data = Object.fromEntries(Object.entries(queries).map(([key, query]) => [key, (query as QueryResult & { data?: unknown }).data])) as {
    [K in keyof T]: T[K] extends { data: infer D } ? D : never;
  };
  const refetch = async () => { await Promise.all(values.map(query => query.refetch())); };
  return { ...data, isPending, isError: Boolean(error), error, refetch };
}
