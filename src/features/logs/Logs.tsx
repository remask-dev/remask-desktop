import { Search, ShieldAlert, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import type { AuditEntity, AuditLog, BootstrapData } from "../../shared/api/types";
import { useI18n } from "../../shared/i18n/I18n";
import { Badge, StatusDot } from "../../shared/ui/Status";
import { Input } from "../../shared/ui/Input";

export function Logs({ data }: { data: BootstrapData }) {
  const { t, dateLocale } = useI18n();
  const [query, setQuery] = useState("");
  const [date, setDate] = useState("");
  const [provider, setProvider] = useState("all");
  const [selected, setSelected] = useState(data.logs[0]?.id || "");
  const providers = useMemo(() => uniqueValues(data.logs.map((log) => log.upstream_id)), [data.logs]);
  const logs = useMemo(() => data.logs.filter((log) => {
    const searchable = (log.upstream_id + (log.model ?? "") + log.path).toLowerCase();
    return (!query || searchable.includes(query.toLowerCase())) &&
      (!date || localDateKey(log.timestamp) === date) &&
      (provider === "all" || log.upstream_id === provider);
  }), [data.logs, query, date, provider]);
  const item = logs.find((log) => log.id === selected) || logs[0];
  const hasFilters = Boolean(date) || provider !== "all";

  return <div className="logs-page">
    <div className="logs-toolbar">
      <label className="search"><Search size={13}/><Input className="h-auto border-0 bg-transparent px-0 shadow-none focus-visible:ring-0" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")}/></label>
      <div className="logs-toolbar__filters">
        <Input className="logs-date-input" type="date" value={date} onChange={(event) => setDate(event.target.value)} aria-label={t("dateFilter")}/>
        <select className="logs-provider-select" value={provider} onChange={(event) => setProvider(event.target.value)} aria-label={t("providerFilter")}><option value="all">{t("allProviders")}</option>{providers.map((value) => <option key={value} value={value}>{value}</option>)}</select>
        {hasFilters && <button type="button" className="logs-filter-clear" aria-label={t("clearFilters")} onClick={() => { setDate(""); setProvider("all"); }}>×</button>}
      </div>
    </div>
    <div className="split-view logs-view">
      <section className="list-pane"><div className="request-list">{logs.map((log) => <button key={log.id} className={item?.id === log.id ? "selected" : ""} onClick={() => setSelected(log.id)}><div><StatusDot tone={isPass(log) ? "warning" : "success"}/><strong>{log.upstream_id}</strong><time>{new Date(log.timestamp).toLocaleString(dateLocale, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" })}</time></div><div className="request-list__route"><code>{log.method} {log.path}</code><span title={log.model}>{log.model || "—"}</span></div><small><span>{log.entity_count} {t("entities")}{log.token_usage?.total ? ` · ${log.token_usage.total} ${t("tokens")}` : ""}</span><span>{log.status_code} · {log.duration_ms} ms{log.streaming ? " · SSE" : ""}</span></small></button>)}</div></section>
      <section className="detail-pane">{item ? <LogDetail item={item}/> : <div className="detail-empty"><FileIcon/><h2>{t("selectRequest")}</h2><p>{t("selectRequestSub")}</p></div>}</section>
    </div>
  </div>;
}

function LogDetail({ item }: { item: AuditLog }) {
  const { t, dateLocale } = useI18n();
  const pass = isPass(item);
  const disabled = item.protection_mode === "disabled" || item.operation_id === "disabled";
  const usage = item.token_usage ?? { input: 0, output: 0, total: 0 };
  const protectionLabel = disabled ? t("protectionDisabled") : pass ? t("passthrough") : t("redacted");
  return <><header className="detail-header"><div><span>{new Date(item.timestamp).toLocaleString(dateLocale)}</span><h2>{item.upstream_id}</h2><code>{item.method} {item.path}</code></div><Badge tone={pass ? "warning" : "success"}>{protectionLabel}</Badge></header><section className="detail-section detail-section--summary"><h3>{t("summary")}</h3><dl className="property-grid property-grid--three"><div><dt>{t("requestModel")}</dt><dd>{item.model || "—"}</dd></div><div><dt>{t("protocol")}</dt><dd>{item.profile_id}</dd></div><div><dt>{t("protection")}</dt><dd className={pass ? "warning" : "success"}>{protectionLabel}</dd></div><div><dt>{t("entityCount")}</dt><dd>{item.entity_count}</dd></div><div><dt>{t("duration")}</dt><dd>{item.duration_ms} ms</dd></div><div><dt>{t("traffic")}</dt><dd>{formatBytes(item.request_bytes)} → {formatBytes(item.response_bytes)}</dd></div><div><dt>{t("tokenUsage")}</dt><dd>{usage.input} + {usage.output} = {usage.total}{usage.cached ? ` · ${usage.cached} ${t("cachedTokens")}` : ""}</dd></div></dl></section>{item.debug && <DebugExchangeView item={item.debug}/>}<section className="detail-section"><h3>{t("fieldAudit")}</h3>{pass ? <div className="warning-callout"><ShieldAlert size={18}/><div><strong>{protectionLabel}</strong><p>{disabled ? t("disabledNote") : t("passthroughNote")}</p></div></div> : (item.fields ?? []).map((field) => { const entities = field.entities ?? []; return <details className="audit-field" key={field.path} open={entities.length > 0}><summary><code>{field.path}</code><span>{entities.length} {t("entities")}</span></summary><div><section><span>{t("originalMasked")}</span><pre>{field.original_masked}</pre></section><section><span>{t("sentToAI")}</span><pre className="entity-rich-text">{renderEntities(field.redacted, entities)}</pre></section></div></details>; })}</section></>;
}

function DebugExchangeView({ item }: { item: NonNullable<AuditLog["debug"]> }) {
  return <section className="detail-section debug-exchange"><h3>Debug</h3><div className="debug-exchange__grid"><DebugPart title={`${item.request.method} ${item.request.url}`} headers={item.request.headers} body={item.request.body}/><DebugPart title={`HTTP ${item.response.status}`} headers={item.response.headers} body={item.response.body}/></div></section>;
}

function DebugPart({ title, headers, body }: { title: string; headers?: Record<string, string[]>; body?: string }) {
  return <article className="debug-part"><header><strong>{title}</strong></header><section><span>Headers</span><pre>{formatHeaders(headers)}</pre></section><section><span>Body</span><pre>{body || "—"}</pre></section></article>;
}

function formatHeaders(headers?: Record<string, string[]>) {
  return Object.entries(headers ?? {}).map(([key, values]) => `${key}: ${values.join(", ")}`).join("\n") || "—";
}

const isPass = (log: AuditLog) => log.protection_mode === "passthrough" || log.protection_mode === "disabled" || log.operation_id === "passthrough" || log.operation_id === "disabled";
const formatBytes = (n: number) => n < 1024 ? `${n} B` : `${(n / 1024).toFixed(1)} KB`;
const uniqueValues = (values: string[]) => Array.from(new Set(values.filter(Boolean))).sort((a, b) => a.localeCompare(b));
function localDateKey(timestamp: string) { const date = new Date(timestamp); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; }
function FileIcon() { return <div className="detail-empty__icon"><ShieldCheck size={24}/></div>; }
function renderEntities(text: string, entities: AuditEntity[]) { let parts: [string, AuditEntity | null][] = [[text, null]]; for (const entity of entities) { const value = entity.replacement; if (!value) continue; parts = parts.flatMap(([part, mark]) => { if (mark) return [[part, mark]]; const index = part.indexOf(value); if (index < 0) return [[part, null]]; return [[part.slice(0, index), null], [value, entity], [part.slice(index + value.length), null]]; }); } return parts.map(([part, entity], index) => entity ? <mark key={`${entity.replacement}-${index}`} title={entity.type}>{part}<small>{entity.type}</small></mark> : part); }
