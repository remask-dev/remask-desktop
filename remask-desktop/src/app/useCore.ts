import { useQuery } from "@tanstack/react-query";
import { coreApi } from "../shared/api/client";
import type { AuditStatsRange } from "../shared/api/types";

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
};

/** Global shell data: cheap liveness/version plus the protection switch. */
export function useCore() {
  const version = useQuery({
    queryKey: queryKeys.version,
    queryFn: coreApi.version,
    refetchInterval: 15_000,
    retry: 1,
  });
  const policy = useQuery({
    queryKey: queryKeys.policy,
    queryFn: coreApi.policy,
    refetchInterval: 15_000,
    retry: 1,
  });

  return {
    version,
    policy,
    isPending: version.isPending,
  };
}

export function useOverviewData(range: AuditStatsRange) {
  const stats = useQuery({ queryKey: queryKeys.stats(range), queryFn: () => coreApi.stats(range), refetchInterval: 15_000, placeholderData: previous => previous });
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy });
  const activeModel = useQuery({ queryKey: queryKeys.activeModel, queryFn: coreApi.activeModel });
  return combineQueries({ stats, policy, activeModel });
}

export function useLogsData(query = "") {
  return useQuery({ queryKey: queryKeys.logs(query), queryFn: () => coreApi.logs(query) });
}

export function useServicesData() {
  const upstreams = useQuery({ queryKey: queryKeys.upstreams, queryFn: coreApi.upstreams });
  const profiles = useQuery({ queryKey: queryKeys.profiles, queryFn: coreApi.profiles });
  const proxyCA = useQuery({ queryKey: queryKeys.proxyCA, queryFn: coreApi.proxyCA });
  return combineQueries({ upstreams, profiles, proxyCA });
}

export function useModelsData() {
  const models = useQuery({ queryKey: queryKeys.models, queryFn: coreApi.models });
  const activeModel = useQuery({ queryKey: queryKeys.activeModel, queryFn: coreApi.activeModel });
  return combineQueries({ models, activeModel });
}

export function useRulesData() {
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy });
  const models = useQuery({ queryKey: queryKeys.models, queryFn: coreApi.models });
  return combineQueries({ policy, models });
}

export function useSettingsData() {
  const settings = useQuery({ queryKey: queryKeys.settings, queryFn: coreApi.settings });
  const policy = useQuery({ queryKey: queryKeys.policy, queryFn: coreApi.policy });
  const upstreams = useQuery({ queryKey: queryKeys.upstreams, queryFn: coreApi.upstreams });
  const proxyCA = useQuery({ queryKey: queryKeys.proxyCA, queryFn: coreApi.proxyCA });
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
