import { Braces, Check, Cpu, ShieldCheck } from "lucide-react";
import { connection } from "../../shared/api/client";
import type { BootstrapData } from "../../shared/api/types";
import { useI18n } from "../../shared/i18n/I18n";
import { useApp } from "../../app/AppContext";

export function Overview({ data }: { data: BootstrapData }) {
  const { t, dateLocale } = useI18n(); const { notify } = useApp(); const proxy=connection.proxy();
  const max=Math.max(1,...data.stats.daily.flatMap(item=>[item.entities,item.requests]));
  const ruleCount=t("ruleCount").replace("{count}",data.policy.rules.length.toLocaleString(dateLocale));
  const tpm=Math.round(data.stats.tokens_per_minute).toLocaleString(dateLocale);
  const cacheHitRate=data.stats.token_total>0?Math.round((data.stats.token_cached/data.stats.token_total)*100):0;
  const modelVersion=data.activeModel?shortVersion(data.activeModel.version):"";
  async function copy(value:string){await navigator.clipboard.writeText(value);notify(t("copied"));}
  return <div className="overview-layout">
    <section className="trust-strip"><div className="trust-strip__title"><ShieldCheck size={16}/><span>{t("path")}</span></div><div className="protection-capabilities"><div className="protection-node"><span className="protection-node__icon"><Braces size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{t("rulesEngine")}</small></span><strong>{ruleCount}</strong></span><CapabilityState/></div><div className="protection-node"><span className="protection-node__icon"><Cpu size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{data.activeModel?`${t("localModel")} · ${modelVersion}`:t("localModel")}</small></span><strong>{data.activeModel?.name||t("unconfigured")}</strong></span><CapabilityState active={Boolean(data.activeModel)}/></div><button className="protection-node protection-node--gateway" aria-label={`${t("copy")} ${t("gateway")}: ${proxy}`} onClick={()=>copy(proxy)}><span className="protection-node__icon"><ShieldCheck size={14}/></span><span className="protection-node__body"><span className="protection-node__label"><small>{t("gateway")}</small></span><span className="protection-node__address"><code>{proxy}</code></span></span><CapabilityState/></button></div></section>
    <div className="overview-workspace overview-workspace--compact"><div className="overview-primary"><section className="metric-row"><Metric label={t("protected")} value={data.stats.entities.toLocaleString(dateLocale)} accent/><Metric label={t("requests")} value={data.stats.requests.toLocaleString(dateLocale)} detail={`${Math.round(data.stats.success_rate*100)}% ${t("success")}`}/><Metric label={t("latency")} value={`${data.stats.average_latency_ms}`} unit="ms" detail={`${data.stats.streaming_requests} ${t("streaming")}`}/><Metric label={t("tpm")} value={tpm} detail={t("tpmSub")}/><Metric label={t("cacheHitRate")} value={`${cacheHitRate}%`} detail={t("cacheHitRateSub")}/></section>
      <section className="surface chart-surface"><SectionTitle title={t("activity")} subtitle={t("activitySub")}/><div className="chart"><div className="chart-grid"><i/><i/><i/></div>{data.stats.daily.map(item=><div className="chart-column" key={item.date}><div className="bars"><b style={{height:`${Math.max(3,item.entities/max*100)}%`}}/><b style={{height:`${Math.max(3,item.requests/max*100)}%`}}/></div><span>{item.date.slice(5)}</span></div>)}</div><div className="chart-legend"><span><i className="green"/>{t("entities")}</span><span><i/>{t("requests")}</span></div></section>
    </div></div>
  </div>;
}
function shortVersion(version:string){return version.length>13?`${version.slice(0,13)}…`:version}
function Metric({label,value,unit,detail,accent}:{label:string;value:string|number;unit?:string;detail?:string;accent?:boolean}){return <article className={`metric ${accent?"metric--accent":""}`}><span>{label}</span><strong>{value}{unit&&<em>{unit}</em>}</strong><small>{detail||" "}</small></article>}
function SectionTitle({title,subtitle}:{title:string;subtitle:string}){return <div className="section-title"><h2>{title}</h2>{subtitle&&<p>{subtitle}</p>}</div>}
function CapabilityState({active=true}:{active?:boolean}){return <span className={`capability-state ${active?"capability-state--active":"capability-state--warning"}`} aria-hidden="true">{active?<Check size={9}/>:<span>!</span>}</span>}
