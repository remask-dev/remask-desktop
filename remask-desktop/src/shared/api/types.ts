export interface RuntimeStatus { available: boolean; name: string; provider?: string; configured_provider?: string; provider_config_pending?: boolean; max_inference_tokens?: number; configured_max_inference_tokens?: number; inference_config_pending?: boolean }
export interface VersionResponse { name: string; version: string; api_version: string; capabilities: string[]; model_runtime: RuntimeStatus }
export interface ProxyCAStatus { ready: boolean; certificate_path?: string; fingerprint_sha256: string }
export interface Profile { id: string; name: string; operations: unknown[]; header_templates?: Record<string, string> }
export interface Upstream { id: string; base_url: string; profile_id: string; credential_mode: "passthrough" | "managed"; api_key?: string; header_templates?: Record<string, string> }
export interface ModelManifest { name: string; version: string; quantization: string; max_tokens: number; stride: number; label_scheme: string; entity_types?: Record<string, string> }
export interface ModelPackage { id: string; valid: boolean; active: boolean; manifest: ModelManifest; errors: string[] }
export interface ActiveModel { id: string; name: string; version: string; runtime: string; quantization: string }
export interface AuditEntity { type: string; replacement: string; masked?: string; confidence: number; sources?: string[] }
export interface AuditField { path: string; original_masked: string; redacted: string; entities: AuditEntity[] | null }
export interface TokenUsage { input: number; output: number; total: number; cached?: number }
export interface AuditLog { id: string; timestamp: string; upstream_id: string; profile_id: string; operation_id: string; protection_mode?: "redacted" | "passthrough" | "disabled"; method: string; path: string; status_code: number; duration_ms: number; streaming: boolean; request_bytes: number; response_bytes: number; entity_count: number; token_usage?: TokenUsage; fields?: AuditField[]; error_code?: string }
export interface DailyStat { date: string; requests: number; entities: number }
export type AuditStatsRange = "today" | "yesterday" | "7d" | "30d";
export interface AuditStats { requests: number; entities: number; success_rate: number; average_latency_ms: number; streaming_requests: number; entity_types: Record<string, number>; daily: DailyStat[]; token_input: number; token_output: number; token_total: number; token_cached: number; tokens_per_minute: number; granularity?: "hour" | "day" }
export interface AuditSettings { record_request_content: boolean; retention_days: number; hf_base_url?: string; max_inference_tokens: number; inference_provider: "auto" | "cpu" | "gpu"; entity_cache_enabled: boolean; entity_cache_ttl_seconds: number }
export interface ModelDownloadRequest { repo: string; revision?: string; variant?: string; id?: string; name?: string; base_url?: string }
export interface ModelCatalogEntry { id: string; name: string; project_url: string; repo: string; revision: string; variant: string }
export interface PIIEntity { type: string; replacement: string; confidence: number; sources: string[] }
export interface RedactResult { text: string; scope_id: string; replacement_count: number; entities: PIIEntity[] }
export interface RuleConfig { id: string; pattern: string; enabled: boolean }
export interface EntityTypeConfig { type: string; enabled: boolean }
export interface PolicySettings { enabled: boolean; redact_ai_answers: boolean; entity_types: EntityTypeConfig[]; rules: RuleConfig[] }
export interface Operation { id: string; status: "pending" | "running" | "succeeded" | "failed" | "cancelled"; progress?: number; message?: string; error?: string }
export interface BootstrapData { version: VersionResponse; proxyCA: ProxyCAStatus; profiles: Profile[]; upstreams: Upstream[]; models: ModelPackage[]; runtime: RuntimeStatus; activeModel: ActiveModel | null; settings: AuditSettings; policy: PolicySettings; stats: AuditStats; logs: AuditLog[] }
