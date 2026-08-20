import { ListPlus, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { ModelPackage, PolicySettings, RuleConfig } from "../../shared/api/types";
import { coreApi } from "../../shared/api/client";
import { queryKeys, useRulesData } from "../../app/useCore";
import { useI18n } from "../../shared/i18n/I18n";
import { useApp } from "../../app/AppContext";
import { Button } from "../../shared/ui/Button";
import { Input } from "../../shared/ui/Input";
import { Switch } from "../../shared/ui/Switch";
import { PageState } from "../../shared/ui/PageState";
import { entityFriendlyLabels, type MessageKey } from "../../shared/i18n/messages";
import { FREE_CUSTOM_RULE_LIMIT, hasAdvancedLicense } from "../../shared/license";
import { AdvancedBadge } from "../../shared/ui/Status";

const blankRule = (): RuleConfig => ({ id: `RULE_${Date.now()}`, pattern: "", enabled: true });
const builtInTypes = ["ACCOUNT_NUMBER", "ADDRESS", "PRIVATE_DATE", "EMAIL_ADDRESS", "PERSON", "PHONE_NUMBER", "URL", "SECRET"];
const entityLabels: Record<string, MessageKey> = {
  PERSON: "entityPerson", PHONE_NUMBER: "entityPhone", EMAIL_ADDRESS: "entityEmail", ADDRESS: "entityAddress",
  PRIVATE_DATE: "entityPrivateDate", ACCOUNT_NUMBER: "entityAccount", IP_ADDRESS: "entityIp",
  SECRET: "entitySecret", URL: "entityUrl",
};
const normalizePolicy = (policy: PolicySettings): PolicySettings => {
  const configured = Array.isArray(policy.entity_types) ? policy.entity_types : [];
  const rules = Array.isArray(policy.rules) ? policy.rules : [];
  const types = [...new Set([...builtInTypes, ...configured.map(item => item.type)])];
  return { ...structuredClone(policy), rules, entity_types: types.map(type => configured.find(item => item.type === type) ?? { type, enabled: true }) };
};

function modelLabelRows(models: ModelPackage[]) {
  const active = models.find(model => model.active);
  const values = Object.values(active?.manifest.entity_types ?? {});
  if (!values.length) return builtInTypes.map(value => ({ value }));
  return [...new Set(values.map(value => value.trim()).filter(Boolean))].map(value => ({ value }));
}

export function Rules() {
  const query = useRulesData();
  if (query.isPending || !query.policy || !query.models) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <RulesContent data={{ policy: query.policy, models: query.models.models, advanced: hasAdvancedLicense(query.license) }}/>;
}

function RulesContent({ data }: { data: { policy: PolicySettings; models: ModelPackage[]; advanced: boolean } }) {
  const { t, locale } = useI18n(); const { notify } = useApp();
  const qc = useQueryClient();
  const [policy, setPolicy] = useState<PolicySettings>(() => normalizePolicy(data.policy));
  const labels = modelLabelRows(data.models);
	const customRuleCount = policy.rules.length;
	const enabledRuleCount = policy.rules.filter(rule => rule.enabled).length;
	const addRule = () => {
		if (!data.advanced && policy.rules.length >= FREE_CUSTOM_RULE_LIMIT) {
			notify(t("advancedRequired"));
			return;
		}
		setPolicy(current => {
			if (!data.advanced && current.rules.length >= FREE_CUSTOM_RULE_LIMIT) return current;
			return { ...current, rules: [...current.rules, blankRule()] };
		});
	};
  const requestVersion = useRef(0);
  const persist = async (next: PolicySettings, announce = false) => {
    const version = ++requestVersion.current;
    const previous = qc.getQueryData<PolicySettings>(queryKeys.policy);
    const optimistic = normalizePolicy(next);
    // Keep the shell switch and this page in sync without refreshing unrelated resources.
    qc.setQueryData(queryKeys.policy, optimistic);
    try {
      const saved = normalizePolicy(await coreApi.savePolicy(next));
      if (version !== requestVersion.current) return;
      setPolicy(saved);
      qc.setQueryData(queryKeys.policy, saved);
      if (announce) notify(t("rulesSaved"));
    } catch (error) {
      if (version === requestVersion.current) {
        setPolicy(normalizePolicy(data.policy));
        qc.setQueryData(queryKeys.policy, previous);
        notify(String(error));
      }
    }
  };
  const patchRule = (index: number, patch: Partial<RuleConfig>) => setPolicy({ ...policy, rules: policy.rules.map((rule, current) => current === index ? { ...rule, ...patch } : rule) });
  const patchEntity = (type: string, enabled: boolean) => {
    const exists = policy.entity_types.some(item => item.type === type);
    const next = {
      ...policy,
      entity_types: exists
        ? policy.entity_types.map(item => item.type === type ? { ...item, enabled } : item)
        : [...policy.entity_types, { type, enabled }],
    };
    setPolicy(next);
    void persist(next);
  };

  return <div className="rules-page">
    <section className="entity-controls"><div className="entity-controls__heading"><span className="rules-icon"><ShieldCheck size={17}/></span><div><h2>{t("entityProtection")}</h2><p>{t("entityProtectionSub")}</p></div></div><div className="entity-toggle-grid">{labels.map(entity => { const messageKey = entityLabels[entity.value]; const raw = entity.value.trim(); const key = raw.toUpperCase(); const label = messageKey ? t(messageKey) : entityFriendlyLabels[locale][key as keyof typeof entityFriendlyLabels.zh] || raw || (locale === "zh" ? "未命名实体" : "Unnamed entity"); const configured = policy.entity_types.find(item => item.type === raw); const enabled = configured?.enabled !== false; return <label key={raw || "unnamed-entity"} className={enabled ? "is-enabled" : ""}><span><strong>{label}</strong><code>{raw || "UNKNOWN"}</code></span><Switch ariaLabel={`${label} · ${raw || "UNKNOWN"}`} checked={enabled} onCheckedChange={checked => patchEntity(raw || "UNKNOWN", checked)}/></label>; })}</div></section>
    <section className="custom-rules">
      <header className="rules-toolbar"><div><span className="rules-icon rules-icon--custom"><ListPlus size={17}/></span><div><h2 className="feature-title-with-badge">{t("customRules")}<AdvancedBadge>{t("advancedPlanBadge")}</AdvancedBadge></h2><p>{enabledRuleCount} / {customRuleCount} {t("enabled")}</p></div></div><Button icon={<Plus size={13}/>} onClick={addRule}>{t("addRule")}</Button></header>
      <div className="rules-container">
        <div className="rules-table"><header><span>{t("enabled")}</span><span>{t("ruleId")}</span><span>{t("expression")}</span><i/></header>{policy.rules.map((rule, index) => <article key={index}><Switch checked={rule.enabled} onCheckedChange={enabled => patchRule(index, { enabled })}/><Input value={rule.id} onChange={event => patchRule(index, { id: event.target.value.toUpperCase() })}/><Input className="font-mono" value={rule.pattern} onChange={event => patchRule(index, { pattern: event.target.value })}/><Button size="icon" variant="ghost" aria-label={t("remove")} onClick={() => setPolicy({ ...policy, rules: policy.rules.filter((_, current) => current !== index) })}><Trash2 size={13}/></Button></article>)}</div>
        <footer className="rules-footer"><Button variant="primary" onClick={() => void persist(policy, true)}>{t("saveRules")}</Button></footer>
      </div>
    </section>
  </div>;
}
