import { Copy, FlaskConical, RotateCcw } from "lucide-react";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useApp } from "../../app/AppContext";
import { coreApi } from "../../shared/api/client";
import type { PIIEntity } from "../../shared/api/types";
import { useI18n } from "../../shared/i18n/I18n";
import { Button } from "../../shared/ui/Button";
import { Textarea } from "../../shared/ui/Textarea";

const SAMPLE = "Olivia Bennett, born April 18, 1987, lives at 742 Evergreen Terrace, Springfield, IL 62704. Contact her at +1 (202) 555-0147 or olivia.bennett@example.com. Account: 4839201756; last login IP: 203.0.113.42.";

export function LocalTest() {
  const { t } = useI18n();
  const { notify } = useApp();
  const [source, setSource] = useState(SAMPLE);
  const redact = useMutation({ mutationFn: () => coreApi.redact(source) });
  const result = redact.data;
  async function copy(value: string) { await navigator.clipboard.writeText(value); notify(t("copied")); }
  function reset() { setSource(SAMPLE); redact.reset(); }

  return <div className="local-test-page">
    <section className="test-workbench">
      <div className="test-pane test-pane--source">
        <header><div><span>{t("testInput")}</span><small>{source.length} {t("characters")}</small></div><Button size="icon" variant="ghost" aria-label={t("resetSample")} onClick={reset}><RotateCcw size={13}/></Button></header>
        <Textarea value={source} onChange={event => { setSource(event.target.value); redact.reset(); }} spellCheck={false} aria-label={t("testInput")}/>
        <footer><small>{t("testInputHint")}</small><Button variant="primary" icon={<FlaskConical size={13}/>} onClick={() => redact.mutate()} disabled={!source.trim() || redact.isPending}>{redact.isPending ? t("processing") : t("run")}</Button></footer>
      </div>
      <div className="test-pane test-pane--result">
        <header><div><span>{t("testOutput")}</span><small>{result ? `${result.replacement_count} ${t("entities")}` : t("ready")}</small></div>{result&&<Button size="icon" variant="ghost" aria-label={t("copy")} onClick={() => copy(result.text)}><Copy size={13}/></Button>}</header>
        <div className={`test-output ${result ? "test-output--ready" : ""}`}>
          {redact.isPending ? <div className="test-output__empty"><FlaskConical size={20}/><span>{t("processing")}</span></div> : redact.error ? <div className="test-output__error">{String(redact.error)}</div> : result ? <div className="annotated-result">{annotateResult(result.text,result.entities)}</div> : <div className="test-output__empty"><FlaskConical size={20}/><span>{t("testEmpty")}</span></div>}
        </div>
      </div>
    </section>
  </div>;
}

function annotateResult(text: string, entities: PIIEntity[]) {
  const byReplacement = new Map(entities.map(entity => [entity.replacement, entity]));
  const replacements = [...byReplacement.keys()].sort((a,b) => b.length-a.length);
  if (!replacements.length) return text;
  const pattern = new RegExp(`(${replacements.map(value => value.replace(/[.*+?^${}()|[\]\\]/g,"\\$&")).join("|")})`,"g");
  return text.split(pattern).map((part,index) => {
    const entity = byReplacement.get(part);
    return entity ? <mark className="result-entity" key={`${part}-${index}`}><small>{entity.type}</small><code>{part}</code></mark> : <span key={index}>{part}</span>;
  });
}
