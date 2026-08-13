import { Box, CheckCircle2, Download, Plus, RefreshCw } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { coreApi } from "../../shared/api/client";
import type { BootstrapData, ModelPackage, Operation } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { useI18n } from "../../shared/i18n/I18n";
import { Badge, StatusDot } from "../../shared/ui/Status";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";

type DownloadState = {
  operationId: string;
  modelId: string;
  status: Operation["status"];
  progress: number;
  error?: string;
};

export function Models({ data }: { data: BootstrapData }) {
  const { t } = useI18n();
  const { notify } = useApp();
  const qc = useQueryClient();
  const [selected, setSelected] = useState(data.activeModel?.id || data.models[0]?.id || "");
  const [manual, setManual] = useState(false);
  const [repo, setRepo] = useState("");
  const [variant, setVariant] = useState("");
  const [downloading, setDownloading] = useState<DownloadState | null>(null);
  const current = data.models.find(item => item.id === selected) || data.models[0];

  const scan = useMutation({ mutationFn: coreApi.scanModels, onSuccess: () => qc.invalidateQueries() });
  const unload = useMutation({ mutationFn: coreApi.unloadModel, onSuccess: () => qc.invalidateQueries() });

  const activate = useMutation({
    mutationFn: coreApi.activateModel,
    onSuccess: async result => { await pollOperation(result.operation_id, () => qc.invalidateQueries()); },
    onError: error => notify(String(error)),
  });

  const download = useMutation({
    mutationFn: coreApi.downloadModel,
    onSuccess: async (result, variables) => {
      setManual(false);
      setDownloading({ operationId: result.operation_id, modelId: result.model_id, status: "pending", progress: 0 });
      try {
        await pollOperation(result.operation_id, () => {
          setSelected(result.model_id);
          qc.invalidateQueries();
          notify(t("downloadComplete"));
        });
      } catch (error) {
        notify(String(error));
      } finally {
        setDownloading(null);
        if (!variables.variant) setVariant("");
      }
    },
    onError: error => { setDownloading(null); notify(String(error)); },
  });

  // Poll the Core operation and surface progress through the list placeholder.
  // A live operation is reported per (status, progress); the resolver runs on
  // success and the caller clears the placeholder afterwards.
  async function pollOperation(operationId: string, onSuccess: () => void) {
    for (let attempt = 0; attempt < 720; attempt++) {
      await new Promise(resolve => setTimeout(resolve, 500));
      const op = await coreApi.operation(operationId);
      setDownloading(prev => prev ? { ...prev, status: op.status, progress: op.progress ?? prev.progress, error: op.error } : prev);
      if (op.status === "succeeded") { onSuccess(); return; }
      if (op.status === "failed") { throw new Error(`${t("downloadFailed")}: ${op.error || ""}`.trim()); }
      if (op.status === "cancelled") { return; }
    }
    throw new Error(t("downloadFailed"));
  }

  function startDownload() {
    download.mutate({ repo: repo.trim(), variant: variant.trim() || undefined });
  }

  return <div className="split-view">
    <section className="list-pane">
      <div className="pane-title">
        <div><span>{t("localModels")}</span><small>{data.runtime.name}</small></div>
        <div>
          <Button size="icon" variant="ghost" onClick={() => setManual(true)} aria-label={t("downloadModel")}><Plus size={14}/></Button>
          <Button size="icon" variant="ghost" onClick={() => scan.mutate()} aria-label={t("scanModels")}><RefreshCw size={14}/></Button>
        </div>
      </div>
      <div className="model-list">
        {downloading && <DownloadingItem state={downloading} repo={repo}/>}
        {data.models.map(item => (
          <button key={item.id} className={current?.id === item.id ? "selected" : ""} onClick={() => setSelected(item.id)}>
            <span className="model-icon"><Box size={16}/></span>
            <div>
              <strong>{item.manifest.name || item.id}</strong>
              <code>{item.id}</code>
              <small>{item.manifest.quantization} · {item.manifest.max_tokens} {t("tokens")}</small>
            </div>
            <StatusDot tone={item.active ? "success" : item.valid ? "muted" : "error"}/>
          </button>
        ))}
      </div>
    </section>
    <section className="detail-pane">
      {current
        ? <ModelDetail model={current} runtimeAvailable={data.runtime.available} onActivate={id => activate.mutate(id)} onUnload={id => unload.mutate(id)} activatePending={activate.isPending}/>
        : <div className="detail-empty"><span className="detail-empty__icon"><Box size={24}/></span><h2>{t("downloadModel")}</h2><p>{t("noModels")}</p></div>}
    </section>
    <Dialog open={manual} title={t("manualModelDownload")} onClose={() => setManual(false)}
      footer={<>
        <Button onClick={() => setManual(false)}>{t("cancel")}</Button>
        <Button variant="primary" disabled={!repo.trim() || download.isPending} onClick={startDownload}>{t("download")}</Button>
      </>}>
      <div className="form-stack">
        <label className="field"><span>{t("huggingFaceProject")}</span><Input placeholder="https://huggingface.co/openai/privacy-filter" value={repo} onChange={event => setRepo(event.target.value)}/></label>
        <label className="field"><span>{t("quantizationOptional")}</span><Input placeholder={t("quantizationHint")} value={variant} onChange={event => setVariant(event.target.value)}/></label>
      </div>
    </Dialog>
  </div>;
}

function DownloadingItem({ state, repo }: { state: DownloadState; repo: string }) {
  const { t } = useI18n();
  const resolving = state.progress >= 85;
  const label = state.status === "failed" ? t("downloadFailed") : resolving ? t("parsing") : state.status === "pending" ? t("queued") : t("downloading");
  return <div className="model-download-item">
    <span className="model-icon"><Download size={16}/></span>
    <div>
      <strong>{label}</strong>
      <code>{repo || state.modelId}</code>
      <div className="model-progress" aria-hidden="true"><i style={{ width: `${Math.max(4, Math.min(100, state.progress))}%` }}/></div>
    </div>
  </div>;
}

function ModelDetail({ model, runtimeAvailable, onActivate, onUnload, activatePending }: {
  model: ModelPackage;
  runtimeAvailable: boolean;
  onActivate: (id: string) => void;
  onUnload: (id: string) => void;
  activatePending: boolean;
}) {
  const { t } = useI18n();
  const manifest = model.manifest;
  return <>
    <header className="detail-header">
      <div>
        <span>{model.active ? t("active") : t("models")}</span>
        <h2>{manifest.name}</h2>
        <code>{model.id}</code>
      </div>
      <div className="header-actions">
        {model.active
          ? <Button variant="danger" onClick={() => onUnload(model.id)}>{t("unload")}</Button>
          : <Button variant="primary" disabled={!model.valid || !runtimeAvailable || activatePending} onClick={() => onActivate(model.id)}>{t("activate")}</Button>}
      </div>
    </header>
    <section className="detail-section detail-section--summary">
      <h3>{t("modelSpecs")}</h3>
      <dl className="property-grid">
        <div><dt>{t("quantization")}</dt><dd>{manifest.quantization}</dd></div>
        <div><dt>{t("version")}</dt><dd><code>{manifest.version}</code></dd></div>
        <div><dt>{t("labelScheme")}</dt><dd>{manifest.label_scheme}</dd></div>
        <div><dt>{t("sequence")}</dt><dd>{manifest.max_tokens} / stride {manifest.stride}</dd></div>
      </dl>
    </section>
    <section className="detail-section">
      <h3>{t("validation")}</h3>
      {model.valid ? <div className="validation-ok"><CheckCircle2 size={16}/>{t("valid")}</div> : model.errors.map(error => <p className="validation-error" key={error}>{error}</p>)}
    </section>
  </>;
}
