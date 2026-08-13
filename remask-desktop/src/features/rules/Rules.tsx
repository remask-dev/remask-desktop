import { ListPlus, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { BootstrapData, EntityTypeConfig, PolicySettings, RuleConfig } from "../../shared/api/types";
import { coreApi } from "../../shared/api/client";
import { useI18n } from "../../shared/i18n/I18n";
import { useApp } from "../../app/AppContext";
import { Button } from "../../shared/ui/Button";
import { Input } from "../../shared/ui/Input";
import { Switch } from "../../shared/ui/Switch";
import type { MessageKey } from "../../shared/i18n/messages";

const blankRule = (): RuleConfig => ({ id: `RULE_${Date.now()}`, pattern: "", enabled: true });
const builtInTypes = ["PERSON", "PHONE_NUMBER", "EMAIL_ADDRESS", "ADDRESS", "PRIVATE_DATE", "ACCOUNT_NUMBER", "IP_ADDRESS", "SECRET", "URL"];
const entityLabels: Record<string, MessageKey> = {
  PERSON: "entityPerson", PHONE_NUMBER: "entityPhone", EMAIL_ADDRESS: "entityEmail", ADDRESS: "entityAddress",
  PRIVATE_DATE: "entityPrivateDate", ACCOUNT_NUMBER: "entityAccount", IP_ADDRESS: "entityIp",
  SECRET: "entitySecret", URL: "entityUrl", CUSTOM: "entityCustom",
};
const normalizePolicy = (policy: PolicySettings): PolicySettings => {
  const configured = policy.entity_types ?? [];
  const types = [...new Set([...builtInTypes, ...configured.map(item => item.type)])];
  return { ...structuredClone(policy), entity_types: types.map(type => configured.find(item => item.type === type) ?? { type, enabled: true }) };
};

export function Rules({ data }: { data: BootstrapData }) {
  const { t } = useI18n(); const { notify } = useApp(); const qc = useQueryClient();
  const [policy, setPolicy] = useState<PolicySettings>(() => normalizePolicy(data.policy));
  const save = useMutation({ mutationFn: () => coreApi.savePolicy(policy), onSuccess: result => { setPolicy(result); qc.invalidateQueries(); notify(t("saveRules")); }, onError: error => notify(String(error)) });
  const patchRule = (index: number, patch: Partial<RuleConfig>) => setPolicy({ ...policy, rules: policy.rules.map((rule, current) => current === index ? { ...rule, ...patch } : rule) });
  const patchEntity = (type: string, enabled: boolean) => setPolicy({ ...policy, entity_types: policy.entity_types.map(item => item.type === type ? { ...item, enabled } : item) });

  return <div className="rules-page">
    <section className="entity-controls"><div className="entity-controls__heading"><span className="rules-icon"><ShieldCheck size={17}/></span><div><h2>{t("entityProtection")}</h2><p>{t("entityProtectionSub")}</p></div></div><div className="entity-toggle-grid">{policy.entity_types.map((entity: EntityTypeConfig) => { const label = t(entityLabels[entity.type] ?? "entityCustom"); return <label key={entity.type} className={entity.enabled ? "is-enabled" : ""}><span><strong>{label}</strong><code>{entity.type}</code></span><Switch ariaLabel={`${label} · ${entity.type}`} checked={entity.enabled} onCheckedChange={enabled => patchEntity(entity.type, enabled)}/></label>; })}</div></section>
    <section className="custom-rules">
      <header className="rules-toolbar"><div><span className="rules-icon rules-icon--custom"><ListPlus size={17}/></span><div><h2>{t("customRules")}</h2><p>{policy.rules.filter(rule => rule.enabled).length} / {policy.rules.length} {t("enabled")}</p></div></div><Button icon={<Plus size={13}/>} onClick={() => setPolicy({ ...policy, rules: [...policy.rules, blankRule()] })}>{t("addRule")}</Button></header>
      <div className="rules-container">
        <div className="rules-table"><header><span>{t("enabled")}</span><span>{t("ruleId")}</span><span>{t("expression")}</span><i/></header>{policy.rules.map((rule, index) => <article key={index}><Switch checked={rule.enabled} onCheckedChange={enabled => patchRule(index, { enabled })}/><Input value={rule.id} onChange={event => patchRule(index, { id: event.target.value.toUpperCase() })}/><Input className="font-mono" value={rule.pattern} onChange={event => patchRule(index, { pattern: event.target.value })}/><Button size="icon" variant="ghost" aria-label={t("remove")} onClick={() => setPolicy({ ...policy, rules: policy.rules.filter((_, current) => current !== index) })}><Trash2 size={13}/></Button></article>)}</div>
        <footer className="rules-footer"><p>{t("deterministicRuleNote")}</p><Button variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>{t("saveRules")}</Button></footer>
      </div>
    </section>
  </div>;
}
