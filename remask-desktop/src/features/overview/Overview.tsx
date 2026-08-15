import { useState } from "react";
import { Braces, Check, Cpu, ShieldCheck } from "lucide-react";
import { connection } from "../../shared/api/client";
import type { AuditStatsRange, DailyStat } from "../../shared/api/types";
import { useI18n } from "../../shared/i18n/I18n";
import { Select } from "../../shared/ui/Select";
import { useApp } from "../../app/AppContext";
import { useOverviewData } from "../../app/useCore";
import { PageState } from "../../shared/ui/PageState";

export function Overview() {
  const { t, dateLocale } = useI18n();
  const { notify } = useApp();
  const proxy = connection.proxy();
  const [range, setRange] = useState<AuditStatsRange>("today");
  const statsQuery = useOverviewData(range);
  if (statsQuery.isPending || !statsQuery.stats || !statsQuery.policy || statsQuery.activeModel === undefined) return <PageState pending={statsQuery.isPending} error={statsQuery.error} onRetry={() => void statsQuery.refetch()}/>;
  const stats = statsQuery.stats;
  const max = Math.max(1, ...stats.daily.flatMap((item) => [item.entities, item.requests]));
  const ruleCount = t("ruleCount").replace("{count}", statsQuery.policy.rules.length.toLocaleString(dateLocale));
  const tpm = Math.round(stats.tokens_per_minute).toLocaleString(dateLocale);
  const cacheHitRate = stats.token_total > 0 ? Math.round((stats.token_cached / stats.token_total) * 100) : 0;
  const rangeOptions = [
    { value: "today", label: t("today") },
    { value: "yesterday", label: t("yesterday") },
    { value: "7d", label: t("last7Days") },
    { value: "30d", label: t("last30Days") },
  ];

  async function copy(value: string) {
    await navigator.clipboard.writeText(value);
    notify(t("copied"));
  }

  return <div className="overview-layout">
    <section className="trust-strip">
      <div className="trust-strip__title"><ShieldCheck size={16}/><span>{t("path")}</span></div>
      <div className="protection-capabilities">
        <div className="protection-node"><span className="protection-node__icon"><Braces size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{t("rulesEngine")}</small></span><strong>{ruleCount}</strong></span><CapabilityState/></div>
        <div className="protection-node"><span className="protection-node__icon"><Cpu size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{t("localModel")}</small></span><strong>{statsQuery.activeModel?.name || t("unconfigured")}</strong></span><CapabilityState active={Boolean(statsQuery.activeModel)}/></div>
        <button className="protection-node protection-node--gateway" aria-label={`${t("copy")} ${t("gateway")}: ${proxy}`} onClick={() => copy(proxy)}><span className="protection-node__icon"><ShieldCheck size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{t("gateway")}</small></span><span className="protection-node__address"><code>{proxy}</code></span></span><CapabilityState/></button>
      </div>
    </section>
    <div className="overview-workspace overview-workspace--compact">
      <div className="overview-primary" aria-busy={statsQuery.isPending}>
        <section className="metric-row">
          <Metric label={t("protected")} value={stats.entities.toLocaleString(dateLocale)} accent/>
          <Metric label={t("requests")} value={stats.requests.toLocaleString(dateLocale)} detail={`${Math.round(stats.success_rate * 100)}% ${t("success")}`}/>
          <Metric label={t("latency")} value={`${stats.average_latency_ms}`} unit="ms" detail={`${stats.streaming_requests} ${t("streaming")}`}/>
          <Metric label={t("tpm")} value={tpm} detail={t("tpmSub")}/>
          <Metric label={t("cacheHitRate")} value={`${cacheHitRate}%`} detail={t("cacheHitRateSub")}/>
        </section>
        <section className="surface chart-surface">
          <div className="chart-heading">
            <SectionTitle title={t("activity")} subtitle={t("activitySub")}/>
            <div className="overview-range-select"><Select value={range} onValueChange={(value) => setRange(value as AuditStatsRange)} options={rangeOptions} ariaLabel={t("timeRange")} className="overview-range-trigger" contentClassName="overview-range-menu"/></div>
          </div>
          <div className="chart"><div className="chart-grid"><i/><i/><i/></div>{stats.daily.map((item, index) => <ChartColumn key={item.date} item={item} index={index} total={stats.daily.length} granularity={stats.granularity ?? "day"} max={max} dateLocale={dateLocale} entitiesLabel={t("entities")} requestsLabel={t("requests")}/>)}</div>
          <div className="chart-legend"><span><i className="green"/>{t("entities")}</span><span><i/>{t("requests")}</span></div>
        </section>
      </div>
    </div>
  </div>;
}

function ChartColumn({ item, index, total, granularity, max, dateLocale, entitiesLabel, requestsLabel }: { item: DailyStat; index: number; total: number; granularity: "hour" | "day"; max: number; dateLocale: string; entitiesLabel: string; requestsLabel: string }) {
  const hourly = granularity === "hour";
  const label = hourly ? item.date.slice(11, 16) : item.date.slice(5);
  const showLabel = hourly ? index % 3 === 0 || index === total - 1 : total <= 7 || index % 5 === 0 || index === total - 1;
  const time = new Date(hourly ? item.date : `${item.date}T00:00:00`);
  const fullLabel = hourly
    ? time.toLocaleString(dateLocale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })
    : time.toLocaleDateString(dateLocale, { month: "short", day: "numeric" });
  const description = `${fullLabel} · ${item.entities} ${entitiesLabel} · ${item.requests} ${requestsLabel}`;
  return <div className="chart-column" title={description} aria-label={description}>
    <div className="bars"><b style={{ height: item.entities ? `${Math.max(3, item.entities / max * 100)}%` : 0 }}/><b style={{ height: item.requests ? `${Math.max(3, item.requests / max * 100)}%` : 0 }}/></div>
    <span>{showLabel ? label : ""}</span>
  </div>;
}

function Metric({ label, value, unit, detail, accent }: { label: string; value: string | number; unit?: string; detail?: string; accent?: boolean }) { return <article className={`metric ${accent ? "metric--accent" : ""}`}><span>{label}</span><strong>{value}{unit && <em>{unit}</em>}</strong><small>{detail || " "}</small></article>; }
function SectionTitle({ title, subtitle }: { title: string; subtitle: string }) { return <div className="section-title"><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div>; }
function CapabilityState({ active = true }: { active?: boolean }) { return <span className={`capability-state ${active ? "capability-state--active" : "capability-state--warning"}`} aria-hidden="true">{active ? <Check size={9}/> : <span>!</span>}</span>; }
