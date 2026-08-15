import { LoaderCircle, RefreshCw, ShieldCheck } from "lucide-react";
import { useI18n } from "../i18n/I18n";
import { Button } from "./Button";

export function PageState({ pending, error, onRetry }: { pending: boolean; error?: unknown; onRetry?: () => void }) {
  const { t } = useI18n();
  if (pending) {
    return <div className="empty-page" aria-busy="true"><LoaderCircle className="core-loading-indicator" size={28} strokeWidth={1.5}/></div>;
  }
  return <div className="empty-page"><div className="empty-page__icon"><ShieldCheck size={26}/></div><h2>{t("error")}</h2><p>{error instanceof Error ? error.message : String(error || t("error"))}</p>{onRetry && <Button variant="secondary" icon={<RefreshCw size={13}/>} onClick={onRetry}>{t("reload")}</Button>}</div>;
}
