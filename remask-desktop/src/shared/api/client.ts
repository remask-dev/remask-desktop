import type { ActiveModel, AuditEntity, AuditField, AuditLog, AuditLogSummary, AuditSettings, AuditStats, AuditStatsRange, DailyStat, EntityTypeConfig, LicenseState, LicenseStatus, ModelDownloadRequest, ModelManifest, ModelPackage, Operation, PIIEntity, PolicySettings, Profile, ProxyCAStatus, ProxyRule, RawRequest, RawResponse, RedactResult, RuleConfig, RuntimeStatus, TokenUsage, Upstream, VersionResponse } from "./types";

const trim = (value: string) => value.trim().replace(/\/$/, "");
export const connection = {
  core: () => import.meta.env.VITE_CORE_URL || "http://127.0.0.1:17680",
  proxyPort: () => {
    const stored = localStorage.getItem("remask.gatewayPort");
    const fallback = Number(import.meta.env.VITE_GATEWAY_PORT) || 17681;
    return stored ? Number(stored) || fallback : fallback;
  },
  proxy: () => `http://127.0.0.1:${connection.proxyPort()}`,
  forwardProxyPort: () => {
    const stored = localStorage.getItem("remask.forwardProxyPort");
    const fallback = Number(import.meta.env.VITE_FORWARD_PROXY_PORT) || 17682;
    return stored ? Number(stored) || fallback : fallback;
  },
  forwardProxy: () => `http://127.0.0.1:${connection.forwardProxyPort()}`,
  socksProxy: () => `socks5h://127.0.0.1:${connection.forwardProxyPort()}`,
  allowLan: () => localStorage.getItem("remask.allowLan") === "true",
  saveGatewayPort(port: number) { localStorage.setItem("remask.gatewayPort", String(port)); },
  saveForwardProxyPort(port: number) { localStorage.setItem("remask.forwardProxyPort", String(port)); },
  saveAllowLan(allow: boolean) { localStorage.setItem("remask.allowLan", String(allow)); },
};

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${connection.core()}${path}`, { ...init, headers: { "Content-Type": "application/json", ...(init.headers || {}) } });
  const contentType = response.headers.get("content-type") || "";
  const body = response.status === 204 ? null : contentType.includes("json") ? await response.json() : await response.text();
  if (!response.ok) throw new Error(body?.error?.message || body || `HTTP ${response.status}`);
  return body as T;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function records(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

function text(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function optionalText(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function number(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function boolean(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function stringRecord(value: unknown): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined;
  return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
}

function headerRecord(value: unknown): Record<string, string[]> | undefined {
  if (!isRecord(value)) return undefined;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, stringList(item)]));
}

function normalizeRuntime(value: unknown): RuntimeStatus {
  const raw = isRecord(value) ? value : {};
  return {
    available: boolean(raw.available),
    name: text(raw.name, "unavailable"),
    provider: optionalText(raw.provider),
    configured_provider: optionalText(raw.configured_provider),
    provider_config_pending: boolean(raw.provider_config_pending),
    max_inference_tokens: number(raw.max_inference_tokens) || undefined,
    configured_max_inference_tokens: number(raw.configured_max_inference_tokens) || undefined,
    inference_config_pending: boolean(raw.inference_config_pending),
  };
}

function normalizeProfile(value: Record<string, unknown>): Profile {
  return {
    id: text(value.id),
    name: text(value.name, text(value.id)),
    operations: Array.isArray(value.operations) ? value.operations : [],
    header_templates: stringRecord(value.header_templates),
  };
}

function normalizeUpstream(value: Record<string, unknown>): Upstream {
  return {
    id: text(value.id),
    base_url: text(value.base_url),
    profile_id: text(value.profile_id),
    credential_mode: value.credential_mode === "managed" ? "managed" : "passthrough",
    api_key: optionalText(value.api_key),
    header_templates: stringRecord(value.header_templates),
    enabled: boolean(value.enabled, true),
  };
}

function normalizeProxyRule(value: Record<string, unknown>): ProxyRule {
  return {
    id: text(value.id), hosts: stringList(value.hosts),
    profile_id: text(value.profile_id), enabled: boolean(value.enabled),
  };
}

function normalizeManifest(value: unknown): ModelManifest {
  const raw = isRecord(value) ? value : {};
  return {
    name: text(raw.name),
    version: text(raw.version),
    quantization: text(raw.quantization),
    max_tokens: number(raw.max_tokens),
    stride: number(raw.stride),
    label_scheme: text(raw.label_scheme),
    entity_types: stringRecord(raw.entity_types),
  };
}

function normalizeModel(value: Record<string, unknown>): ModelPackage {
  return {
    id: text(value.id),
    valid: boolean(value.valid),
    active: boolean(value.active),
    built_in: boolean(value.built_in),
    manifest: normalizeManifest(value.manifest),
    errors: stringList(value.errors),
  };
}

function normalizeActiveModel(value: unknown): ActiveModel | null {
  if (!isRecord(value)) return null;
  return {
    id: text(value.id),
    name: text(value.name, text(value.id)),
    version: text(value.version),
    runtime: text(value.runtime),
    quantization: text(value.quantization),
  };
}

function normalizePolicy(value: unknown): PolicySettings {
  const raw = isRecord(value) ? value : {};
  const entityTypes: EntityTypeConfig[] = records(raw.entity_types).map(item => ({ type: text(item.type), enabled: boolean(item.enabled, true) }));
  const rules: RuleConfig[] = records(raw.rules).map(item => ({ id: text(item.id), pattern: text(item.pattern), enabled: boolean(item.enabled, true) }));
  return { enabled: boolean(raw.enabled, true), redact_ai_answers: boolean(raw.redact_ai_answers), redact_system_messages: boolean(raw.redact_system_messages), entity_types: entityTypes, rules };
}

function normalizeAuditSettings(value: unknown): AuditSettings {
  const raw = isRecord(value) ? value : {};
  const provider = raw.inference_provider;
  return {
    record_request_content: boolean(raw.record_request_content),
    record_raw_request: boolean(raw.record_raw_request),
    retention_days: number(raw.retention_days, 30),
    hf_base_url: optionalText(raw.hf_base_url),
    max_inference_tokens: number(raw.max_inference_tokens, 512),
    inference_provider: provider === "auto" || provider === "gpu" ? provider : "cpu",
    entity_cache_enabled: boolean(raw.entity_cache_enabled, true),
    entity_cache_ttl_seconds: number(raw.entity_cache_ttl_seconds, 900),
  };
}

function normalizeLicense(value: unknown): LicenseState {
  const raw = isRecord(value) ? value : {};
  const statuses: LicenseStatus[] = ["missing", "valid", "expired", "not_yet_valid", "device_mismatch", "invalid", "key_unconfigured", "device_unavailable"];
  const status = statuses.includes(raw.status as LicenseStatus) ? raw.status as LicenseStatus : "invalid";
  return {
    device_id: text(raw.device_id), status, license_id: optionalText(raw.license_id), edition: optionalText(raw.edition), email: optionalText(raw.email),
    issued_at: optionalText(raw.issued_at), not_before: optionalText(raw.not_before), expires_at: optionalText(raw.expires_at),
    features: stringList(raw.features), code: optionalText(raw.code),
  };
}

function normalizeStats(value: unknown): AuditStats {
  const raw = isRecord(value) ? value : {};
  const daily: DailyStat[] = records(raw.daily).map(item => ({ date: text(item.date), requests: number(item.requests), entities: number(item.entities) }));
  const entityTypes = isRecord(raw.entity_types)
    ? Object.fromEntries(Object.entries(raw.entity_types).map(([key, item]) => [key, number(item)]))
    : {};
  return {
    requests: number(raw.requests), entities: number(raw.entities), success_rate: number(raw.success_rate), average_latency_ms: number(raw.average_latency_ms),
    streaming_requests: number(raw.streaming_requests), entity_types: entityTypes, daily, token_input: number(raw.token_input), token_output: number(raw.token_output),
    token_total: number(raw.token_total), token_cached: number(raw.token_cached), tokens_per_minute: number(raw.tokens_per_minute),
    granularity: raw.granularity === "hour" ? "hour" : raw.granularity === "day" ? "day" : undefined,
  };
}

function normalizePIIEntity(value: Record<string, unknown>): PIIEntity {
  return { type: text(value.type, "UNKNOWN"), replacement: text(value.replacement), confidence: number(value.confidence), sources: stringList(value.sources) };
}

function normalizeAuditEntity(value: Record<string, unknown>): AuditEntity {
  return { ...normalizePIIEntity(value), masked: optionalText(value.masked) };
}

function normalizeAuditField(value: Record<string, unknown>): AuditField {
  return { path: text(value.path), original_masked: text(value.original_masked), redacted: text(value.redacted), entities: records(value.entities).map(normalizeAuditEntity) };
}

function normalizeTokenUsage(value: unknown): TokenUsage | undefined {
  if (!isRecord(value)) return undefined;
  return { input: number(value.input), output: number(value.output), total: number(value.total), cached: number(value.cached) || undefined };
}

function normalizeRawRequest(value: unknown): RawRequest | undefined {
	if (!isRecord(value)) return undefined;
	return { method: text(value.method), url: text(value.url), headers: headerRecord(value.headers), body: optionalText(value.body) };
}

function normalizeRawResponse(value: unknown): RawResponse | undefined {
	if (!isRecord(value)) return undefined;
	return { status: number(value.status), headers: headerRecord(value.headers), body: optionalText(value.body) };
}

function normalizeLogSummary(log: Record<string, unknown>): AuditLogSummary {
  const upstreamID = text(log.upstream_id);
  return {
    id: text(log.id), timestamp: text(log.timestamp), upstream_id: upstreamID, profile_id: text(log.profile_id), operation_id: text(log.operation_id),
    model: optionalText(log.model), protection_mode: log.protection_mode === "redacted" || log.protection_mode === "passthrough" || log.protection_mode === "disabled" ? log.protection_mode : undefined,
    gateway_type: log.gateway_type === "proxy_gateway" ? "proxy_gateway" : "api_gateway",
    target_host: optionalText(log.target_host),
    method: text(log.method), path: text(log.path), status_code: number(log.status_code), duration_ms: number(log.duration_ms), streaming: boolean(log.streaming),
    request_bytes: number(log.request_bytes), response_bytes: number(log.response_bytes), entity_count: number(log.entity_count), token_usage: normalizeTokenUsage(log.token_usage),
    error_code: optionalText(log.error_code),
  };
}

function normalizeLogs(value: unknown): AuditLogSummary[] {
  return records(value).map(normalizeLogSummary);
}

function normalizeLog(value: unknown): AuditLog {
  const log = isRecord(value) ? value : {};
	return { ...normalizeLogSummary(log), fields: records(log.fields).map(normalizeAuditField), raw_request: normalizeRawRequest(log.raw_request), raw_response: normalizeRawResponse(log.raw_response) };
}

export const coreApi = {
  // Keep transport calls small and composable. Page hooks decide which of these
  // resources are needed; there is intentionally no all-pages bootstrap call.
  version: () => request<VersionResponse>("/api/v1/version"),
  license: async () => normalizeLicense(await request<unknown>("/api/v1/license")),
  importLicense: async (content: string) => normalizeLicense(await request<unknown>("/api/v1/license/import", { method: "POST", body: JSON.stringify({ content }) })),
  proxyCA: async () => {
    const raw = await request<unknown>("/api/v1/proxy/ca");
    const value = isRecord(raw) ? raw : {};
    return { ready: boolean(value.ready), certificate_path: optionalText(value.certificate_path), fingerprint_sha256: text(value.fingerprint_sha256) } satisfies ProxyCAStatus;
  },
  profiles: async () => { const raw = await request<unknown>("/api/v1/profiles"); return records(isRecord(raw) ? raw.profiles : undefined).map(normalizeProfile); },
  upstreams: async () => { const raw = await request<unknown>("/api/v1/upstreams"); return records(isRecord(raw) ? raw.upstreams : undefined).map(normalizeUpstream); },
  proxyRules: async () => { const raw = await request<unknown>("/api/v1/proxy-rules"); return records(isRecord(raw) ? raw.proxy_rules : undefined).map(normalizeProxyRule); },
  models: async () => { const raw = await request<unknown>("/api/v1/models"); const value = isRecord(raw) ? raw : {}; return { models: records(value.models).map(normalizeModel), runtime: normalizeRuntime(value.runtime) }; },
  activeModel: async () => {
    const raw = await request<unknown>("/api/v1/models/active");
    return isRecord(raw) && raw.active === true ? normalizeActiveModel(raw.model) : null;
  },
  settings: async () => { const raw = await request<unknown>("/api/v1/settings"); return normalizeAuditSettings(isRecord(raw) ? raw.audit : undefined); },
  policy: async () => normalizePolicy(await request<unknown>("/api/v1/policy")),
  stats: async (range: AuditStatsRange) => normalizeStats(await request<unknown>(`/api/v1/audit/stats?range=${range}`)),
  logs: async (query = "") => { const raw = await request<unknown>(`/api/v1/audit/logs?limit=100${query}`); return normalizeLogs(isRecord(raw) ? raw.logs : undefined); },
  log: async (id: string) => { const raw = await request<unknown>(`/api/v1/audit/logs/${encodeURIComponent(id)}`); return normalizeLog(isRecord(raw) ? raw.log : undefined); },
  clearLogs: () => request<void>("/api/v1/audit/logs", { method: "DELETE" }),
  redact: async (source: string) => { const raw = await request<unknown>("/api/v1/redact", { method: "POST", body: JSON.stringify({ text: source }) }); const value = isRecord(raw) ? raw : {}; return { text: text(value.text, source), scope_id: text(value.scope_id), replacement_count: number(value.replacement_count), entities: records(value.entities).map(normalizePIIEntity) } satisfies RedactResult; },
  saveSettings: async (audit: AuditSettings) => { const raw = await request<unknown>("/api/v1/settings", { method: "PUT", body: JSON.stringify({ audit, models: { hf_base_url: audit.hf_base_url || "" } }) }); return { audit: normalizeAuditSettings(isRecord(raw) ? raw.audit : undefined) }; },
  savePolicy: async (policy: PolicySettings) => normalizePolicy(await request<unknown>("/api/v1/policy", { method: "PUT", body: JSON.stringify(policy) })),
  updatePolicy: async (patch: Partial<PolicySettings>) => normalizePolicy(await request<unknown>("/api/v1/policy", { method: "PUT", body: JSON.stringify(patch) })),
  putUpstream: (item: Upstream, editing: boolean) => request<Upstream>(editing ? `/api/v1/upstreams/${encodeURIComponent(item.id)}` : "/api/v1/upstreams", { method: editing ? "PUT" : "POST", body: JSON.stringify(item) }),
  deleteUpstream: (id: string) => request<void>(`/api/v1/upstreams/${encodeURIComponent(id)}`, { method: "DELETE" }),
  putProxyRule: (item: ProxyRule, editing: boolean) => request<ProxyRule>(editing ? `/api/v1/proxy-rules/${encodeURIComponent(item.id)}` : "/api/v1/proxy-rules", { method: editing ? "PUT" : "POST", body: JSON.stringify(item) }),
  deleteProxyRule: (id: string) => request<void>(`/api/v1/proxy-rules/${encodeURIComponent(id)}`, { method: "DELETE" }),
  scanModels: async () => { const raw = await request<unknown>("/api/v1/models/scan", { method: "POST", body: "{}" }); return { models: records(isRecord(raw) ? raw.models : undefined).map(normalizeModel) }; },
  downloadModel: (input: ModelDownloadRequest) => request<{operation_id: string; model_id: string}>("/api/v1/models/download", { method: "POST", body: JSON.stringify(input) }),
  activateModel: (id: string) => request<{operation_id: string}>(`/api/v1/models/${encodeURIComponent(id)}/activate`, { method: "POST", body: "{}" }),
  unloadModel: (id: string) => request<void>(`/api/v1/models/${encodeURIComponent(id)}/unload`, { method: "POST", body: "{}" }),
  deleteModel: (id: string) => request<void>(`/api/v1/models/${encodeURIComponent(id)}`, { method: "DELETE" }),
  operation: (id: string) => request<Operation>(`/api/v1/operations/${encodeURIComponent(id)}`)
};
