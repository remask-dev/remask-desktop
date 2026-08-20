import { Copy, Search, ShieldAlert, ShieldCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { AuditEntity, AuditLog, AuditLogSummary } from "../../shared/api/types";
import { useI18n } from "../../shared/i18n/I18n";
import { Badge, StatusDot } from "../../shared/ui/Status";
import { Input } from "../../shared/ui/Input";
import { useLogDetailData, useLogsData } from "../../app/useCore";
import { PageState } from "../../shared/ui/PageState";
import { useApp } from "../../app/AppContext";

// Bump the storage key when filter semantics change so an old persisted date
// cannot silently hide all newly recorded requests after an app upgrade.
const LOG_FILTER_STORAGE_KEY = "remask.logs.filters.v2";
type StoredLogFilters = { query: string; date: string; protection: ProtectionFilter };

function readStoredLogFilters(): StoredLogFilters {
  try {
    const raw = localStorage.getItem(LOG_FILTER_STORAGE_KEY);
    if (!raw) return { query: "", date: "", protection: "all" };
    const parsed = JSON.parse(raw) as Partial<StoredLogFilters>;
    return {
      query: typeof parsed.query === "string" ? parsed.query : "",
      date: typeof parsed.date === "string" ? parsed.date : "",
      protection: parsed.protection === "redacted" || parsed.protection === "unredacted" ? parsed.protection : "all",
    };
  } catch {
    return { query: "", date: "", protection: "all" };
  }
}

export function Logs() {
  const { t, dateLocale } = useI18n();
  const logsQuery = useLogsData();
  if (logsQuery.isPending || logsQuery.isError || !logsQuery.data) return <PageState pending={logsQuery.isPending} error={logsQuery.error} onRetry={() => void logsQuery.refetch()}/>;
  return <LogsContent logs={logsQuery.data} t={t} dateLocale={dateLocale}/>;
}

function LogsContent({ logs: sourceLogs, t, dateLocale }: { logs: AuditLogSummary[]; t: ReturnType<typeof useI18n>["t"]; dateLocale: string }) {
  const [initialFilters] = useState(readStoredLogFilters);
  const [query, setQuery] = useState(initialFilters.query);
  const [date, setDate] = useState(initialFilters.date);
  const [protection, setProtection] = useState<ProtectionFilter>(initialFilters.protection);
  const [selected, setSelected] = useState("");
  useEffect(() => {
    try { localStorage.setItem(LOG_FILTER_STORAGE_KEY, JSON.stringify({ query, date, protection } satisfies StoredLogFilters)); } catch { /* storage can be unavailable in restricted webviews */ }
  }, [query, date, protection]);
  const logs = useMemo(() => sourceLogs.filter((log) => {
    const searchable = (log.upstream_id + (log.model ?? "") + (log.target_host ?? "") + log.path).toLowerCase();
    return (!query || searchable.includes(query.toLowerCase())) &&
      (!date || localDateKey(log.timestamp) === date) &&
      (protection === "all" || (protection === "redacted" ? !isPass(log) : isPass(log)));
  }), [sourceLogs, query, date, protection]);
  const item = logs.find((log) => log.id === selected);
  const detailQuery = useLogDetailData(item?.id ?? "");

  return <div className="logs-page">
    <div className="logs-toolbar">
      <label className="search"><Search size={13}/><Input className="h-auto border-0 bg-transparent px-0 shadow-none focus-visible:ring-0" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")}/></label>
      <div className="logs-toolbar__filters">
        <Input className="logs-date-input" type="date" value={date} onChange={(event) => setDate(event.target.value)} aria-label={t("dateFilter")}/>
        <select className="logs-protection-select" value={protection} onChange={(event) => setProtection(event.target.value as ProtectionFilter)} aria-label={t("protectionFilter")}><option value="all">{t("allProtectionModes")}</option><option value="redacted">{t("redacted")}</option><option value="unredacted">{t("unredacted")}</option></select>
      </div>
    </div>
    <div className="split-view logs-view">
      <section className="list-pane"><div className="request-list">{logs.map((log) => <button key={log.id} className={item?.id === log.id ? "selected" : ""} onClick={() => setSelected(log.id)}><div><StatusDot tone={isPass(log) ? "muted" : "success"}/><strong>{log.upstream_id}</strong><time>{new Date(log.timestamp).toLocaleString(dateLocale, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" })}</time></div><div className="request-list__route"><code>{log.method} {log.path}</code><span title={log.model}>{log.model || "—"}</span></div><small><span>{log.entity_count} {t("entities")}{log.token_usage?.total ? ` · ${log.token_usage.total} ${t("tokens")}` : ""}</span><span>{log.status_code} · {log.duration_ms} ms{log.streaming ? " · SSE" : ""}</span></small></button>)}</div></section>
      <section className="detail-pane">{item ? (detailQuery.isPending ? <PageState pending error={detailQuery.error}/> : detailQuery.data ? <LogDetail item={detailQuery.data}/> : <PageState pending={false} error={detailQuery.error} onRetry={() => void detailQuery.refetch()}/>) : <div className="detail-empty" role="img" aria-label={t("selectRequest")}><FileIcon/></div>}</section>
    </div>
  </div>;
}

type ProtectionFilter = "all" | "redacted" | "unredacted";

function LogDetail({ item }: { item: AuditLog }) {
  const { t, dateLocale } = useI18n();
  const pass = isPass(item);
  const disabled = item.protection_mode === "disabled" || item.operation_id === "disabled";
  const usage = item.token_usage ?? { input: 0, output: 0, total: 0 };
  const protectionLabel = disabled ? t("protectionDisabled") : pass ? t("passthrough") : t("redacted");
  const gateway = gatewayLabel(item, t);
  const traffic = `${formatBytes(item.request_bytes)} → ${formatBytes(item.response_bytes)}`;
  const tokenUsage = `${usage.input} + ${usage.output} = ${usage.total}${usage.cached ? ` · ${usage.cached} ${t("cachedTokens")}` : ""}`;
  return <>
    <header className="detail-header">
      <div><span>{new Date(item.timestamp).toLocaleString(dateLocale)}</span><h2>{item.upstream_id}</h2><code>{item.method} {item.path}</code></div>
      <div className="detail-header__badges"><Badge>{gateway}</Badge><Badge tone={pass ? "warning" : "success"}>{protectionLabel}</Badge></div>
    </header>
    <section className="detail-section detail-section--summary">
      <h3>{t("summary")}</h3>
      <dl className="property-grid property-grid--three">
        <div><dt>{t("requestModel")}</dt><dd title={item.model || "—"}>{item.model || "—"}</dd></div>
        <div><dt>{t("gatewayType")}</dt><dd title={gateway}>{gateway}</dd></div>
        <div><dt>{t("targetDomain")}</dt><dd title={item.target_host || "—"}>{item.target_host || "—"}</dd></div>
        <div><dt>{t("protocol")}</dt><dd title={item.profile_id}>{item.profile_id}</dd></div>
        <div><dt>{t("protection")}</dt><dd className={pass ? "warning" : "success"} title={protectionLabel}>{protectionLabel}</dd></div>
        <div><dt>{t("entityCount")}</dt><dd title={`${item.entity_count}`}>{item.entity_count}</dd></div>
        <div><dt>{t("duration")}</dt><dd title={`${item.duration_ms} ms`}>{item.duration_ms} ms</dd></div>
        <div><dt>{t("traffic")}</dt><dd title={traffic}>{traffic}</dd></div>
        <div><dt>{t("tokenUsage")}</dt><dd title={tokenUsage}>{tokenUsage}</dd></div>
      </dl>
    </section>
	{item.raw_request && item.raw_response && <RawExchangeView request={item.raw_request} response={item.raw_response}/>}
    <section className="detail-section">
      <h3>{t("fieldAudit")}</h3>
      {pass ? <div className="warning-callout"><ShieldAlert size={18}/><div><strong>{protectionLabel}</strong><p>{disabled ? t("disabledNote") : t("passthroughNote")}</p></div></div> : (item.fields ?? []).map((field) => {
        const entities = field.entities ?? [];
        return <details className="audit-field" key={field.path} open={entities.length > 0}>
          <summary><code>{field.path}</code><span>{entities.length} {t("entities")}</span></summary>
          <div><section><span>{t("originalMasked")}</span><pre>{field.original_masked}</pre></section><section><span>{t("sentToAI")}</span><pre className="entity-rich-text">{renderEntities(field.redacted, entities)}</pre></section></div>
        </details>;
      })}
    </section>
  </>;
}

function RawExchangeView({ request, response }: { request: NonNullable<AuditLog["raw_request"]>; response: NonNullable<AuditLog["raw_response"]> }) {
	const { t } = useI18n();
	return <details className="detail-section raw-exchange raw-exchange--collapsible">
		<summary>{t("rawRequest")}</summary>
		<div className="raw-exchange__body"><div className="raw-exchange__grid"><RawPart title={`${request.method} ${request.url}`} headers={request.headers} body={request.body}/><RawPart title={`HTTP ${response.status}`} headers={response.headers} body={response.body}/></div></div>
	</details>;
}

function RawPart({ title, headers, body }: { title: string; headers?: Record<string, string[]>; body?: string }) {
  const { t } = useI18n();
  const { notify } = useApp();
  const headersText = formatHeaders(headers);
  const bodyText = body || "—";
  async function copyPart(content: string) {
    await navigator.clipboard.writeText(content);
    notify(t("copied"));
  }
	return <article className="raw-part"><header><strong>{title}</strong></header><section><div className="raw-part__section-heading"><span>Headers</span><button className="raw-part__copy" type="button" aria-label={`${t("copy")} Headers`} title={t("copy")} onClick={() => void copyPart(headersText)}><Copy size={10}/></button></div><pre>{headersText}</pre></section><section><div className="raw-part__section-heading"><span>Body</span><button className="raw-part__copy" type="button" aria-label={`${t("copy")} Body`} title={t("copy")} onClick={() => void copyPart(bodyText)}><Copy size={10}/></button></div><pre>{bodyText}</pre></section></article>;
}

function formatHeaders(headers?: Record<string, string[]>) {
  return Object.entries(headers ?? {}).map(([key, values]) => `${key}: ${values.join(", ")}`).join("\n") || "—";
}

const isPass = (log: AuditLog) => log.protection_mode === "passthrough" || log.protection_mode === "disabled" || log.operation_id === "passthrough" || log.operation_id === "disabled";
const gatewayLabel = (log: AuditLogSummary, t: ReturnType<typeof useI18n>["t"]) => t(log.gateway_type === "proxy_gateway" ? "proxyGateway" : "apiGateway");
const formatBytes = (n: number) => n < 1024 ? `${n} B` : `${(n / 1024).toFixed(1)} KB`;
function localDateKey(timestamp: string) { const date = new Date(timestamp); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; }
function FileIcon() { return <div className="detail-empty__icon"><ShieldCheck size={24}/></div>; }
function renderEntities(text: string, entities: AuditEntity[]) { let parts: [string, AuditEntity | null][] = [[text, null]]; for (const entity of entities) { const value = entity.replacement; if (!value) continue; parts = parts.flatMap(([part, mark]) => { if (mark) return [[part, mark]]; const index = part.indexOf(value); if (index < 0) return [[part, null]]; return [[part.slice(0, index), null], [value, entity], [part.slice(index + value.length), null]]; }); } return parts.map(([part, entity], index) => entity ? <mark key={`${entity.replacement}-${index}`} title={entity.type}>{part}<small>{entity.type}</small></mark> : part); }
