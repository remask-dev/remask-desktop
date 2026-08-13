import type { ActiveModel, AuditLog, AuditSettings, AuditStats, BootstrapData, ModelCatalogEntry, ModelDownloadRequest, ModelPackage, Operation, PolicySettings, Profile, RedactResult, RuntimeStatus, Upstream, VersionResponse } from "./types";

const trim = (value: string) => value.trim().replace(/\/$/, "");
export const connection = {
  core: () => "http://127.0.0.1:17680",
  proxyPort: () => {
    const stored = localStorage.getItem("remask.gatewayPort");
    return stored ? Number(stored) || 17681 : 17681;
  },
  proxy: () => `http://127.0.0.1:${connection.proxyPort()}`,
  saveGatewayPort(port: number) { localStorage.setItem("remask.gatewayPort", String(port)); }
};

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${connection.core()}${path}`, { ...init, headers: { "Content-Type": "application/json", ...(init.headers || {}) } });
  const contentType = response.headers.get("content-type") || "";
  const body = response.status === 204 ? null : contentType.includes("json") ? await response.json() : await response.text();
  if (!response.ok) throw new Error(body?.error?.message || body || `HTTP ${response.status}`);
  return body as T;
}

function normalizeLogs(logs: AuditLog[] | null | undefined): AuditLog[] {
  return (logs ?? []).map((log) => ({
    ...log,
    fields: (log.fields ?? []).filter(Boolean).map((field) => ({ ...field, entities: field.entities ?? [] }))
  }));
}

export const coreApi = {
  async bootstrap(days: number): Promise<BootstrapData> {
    const [version, profiles, upstreams, models, modelCatalog, active, settings, policy, stats, logs] = await Promise.all([
      request<VersionResponse>("/api/v1/version"), request<{profiles: Profile[]}>("/api/v1/profiles"), request<{upstreams: Upstream[]}>("/api/v1/upstreams"),
      request<{models: ModelPackage[]; runtime: RuntimeStatus}>("/api/v1/models"), request<{models: ModelCatalogEntry[]}>("/api/v1/models/catalog"), request<{active: boolean; model: ActiveModel; runtime: RuntimeStatus}>("/api/v1/models/active"),
      request<{audit: AuditSettings}>("/api/v1/settings"), request<PolicySettings>("/api/v1/policy"), request<AuditStats>(`/api/v1/audit/stats?days=${days}`), request<{logs: AuditLog[]}>("/api/v1/audit/logs?limit=100")
    ]);
    return { version, profiles: profiles.profiles, upstreams: upstreams.upstreams, models: models.models, modelCatalog: modelCatalog.models, runtime: models.runtime, activeModel: active.active ? active.model : null, settings: settings.audit, policy, stats, logs: normalizeLogs(logs.logs) };
  },
  stats: (days: number) => request<AuditStats>(`/api/v1/audit/stats?days=${days}`),
  logs: async (query = "") => { const result = await request<{logs: AuditLog[]}>(`/api/v1/audit/logs?limit=100${query}`); return { logs: normalizeLogs(result.logs) }; },
  clearLogs: () => request<void>("/api/v1/audit/logs", { method: "DELETE" }),
  redact: (text: string) => request<RedactResult>("/api/v1/redact", { method: "POST", body: JSON.stringify({ text }) }),
  saveSettings: (audit: AuditSettings) => request<{audit: AuditSettings}>("/api/v1/settings", { method: "PUT", body: JSON.stringify({ audit, models: { hf_base_url: audit.hf_base_url || "" } }) }),
  savePolicy: (policy: PolicySettings) => request<PolicySettings>("/api/v1/policy", { method: "PUT", body: JSON.stringify(policy) }),
  putUpstream: (item: Upstream, editing: boolean) => request<Upstream>(editing ? `/api/v1/upstreams/${encodeURIComponent(item.id)}` : "/api/v1/upstreams", { method: editing ? "PUT" : "POST", body: JSON.stringify(item) }),
  deleteUpstream: (id: string) => request<void>(`/api/v1/upstreams/${encodeURIComponent(id)}`, { method: "DELETE" }),
  scanModels: () => request<{models: ModelPackage[]}>("/api/v1/models/scan", { method: "POST", body: "{}" }),
  downloadModel: (input: ModelDownloadRequest) => request<{operation_id: string; model_id: string}>("/api/v1/models/download", { method: "POST", body: JSON.stringify(input) }),
  activateModel: (id: string) => request<{operation_id: string}>(`/api/v1/models/${encodeURIComponent(id)}/activate`, { method: "POST", body: "{}" }),
  unloadModel: (id: string) => request<void>(`/api/v1/models/${encodeURIComponent(id)}/unload`, { method: "POST", body: "{}" }),
  operation: (id: string) => request<Operation>(`/api/v1/operations/${encodeURIComponent(id)}`)
};
