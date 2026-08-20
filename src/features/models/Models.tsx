import { Box, CheckCircle2, Plus, RefreshCw, RotateCw, Trash2 } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { coreApi } from "../../shared/api/client";
import type { ActiveModel, ModelPackage, Operation, RuntimeStatus } from "../../shared/api/types";
import { useApp } from "../../app/AppContext";
import { queryKeys, useModelsData } from "../../app/useCore";
import { useI18n } from "../../shared/i18n/I18n";
import { Badge, StatusDot } from "../../shared/ui/Status";
import { Button } from "../../shared/ui/Button";
import { Dialog } from "../../shared/ui/Dialog";
import { Input } from "../../shared/ui/Input";
import { PageState } from "../../shared/ui/PageState";
import { hasAdvancedLicense } from "../../shared/license";
import { AdvancedBadge } from "../../shared/ui/Status";

type PendingDownload = {
  modelId: string;
  name: string;
  quantization: string;
  operationId: string;
  status: Operation["status"];
  error?: string;
  repo: string;
  variant?: string;
};

type ModelsData = { models: ModelPackage[]; runtime: RuntimeStatus; activeModel: ActiveModel | null };

export function Models() {
  const query = useModelsData();
  if (query.isPending || !query.models || query.activeModel === undefined) return <PageState pending={query.isPending} error={query.error} onRetry={() => void query.refetch()}/>;
  return <ModelsContent data={{ models: query.models.models, runtime: query.models.runtime, activeModel: query.activeModel }} advanced={hasAdvancedLicense(query.license)}/>;
}

function ModelsContent({ data, advanced }: { data: ModelsData; advanced: boolean }) {
  const { t } = useI18n();
  const { notify } = useApp();
  const qc = useQueryClient();
  const [selected, setSelected] = useState(data.activeModel?.id || data.models[0]?.id || "");
  const [manual, setManual] = useState(false);
  const [repo, setRepo] = useState("");
  const [variant, setVariant] = useState("");
  const [pending, setPending] = useState<PendingDownload | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const current = data.models.find(item => item.id === selected) || data.models[0];
  const pendingSelected = pending !== null && selected === pending.modelId;
	const requireAdvanced = (action: () => void, builtIn = false) => advanced || builtIn ? action() : notify(t("advancedRequired"));

  const invalidateModels = () => Promise.all([qc.invalidateQueries({ queryKey: queryKeys.models }), qc.invalidateQueries({ queryKey: queryKeys.activeModel })]);
  const scan = useMutation({ mutationFn: coreApi.scanModels, onSuccess: invalidateModels });
  const unload = useMutation({ mutationFn: coreApi.unloadModel, onSuccess: invalidateModels });
  const remove = useMutation({
    mutationFn: coreApi.deleteModel,
    onSuccess: () => { setDeleteTarget(null); setSelected(""); void invalidateModels(); },
    onError: error => notify(String(error)),
  });

  const activate = useMutation({
    mutationFn: coreApi.activateModel,
    onSuccess: async result => { await pollOperation(result.operation_id, () => { void invalidateModels(); }); },
    onError: error => notify(String(error)),
  });

  // The backend resolves and validates the repository before this mutation
  // resolves, so a success here means the model is known to be downloadable:
  // it appears in the list immediately with a "downloading" state.
  const download = useMutation({
    mutationFn: coreApi.downloadModel,
    onSuccess: async (result, variables) => {
      setManual(false);
      setPending({
        modelId: result.model_id, name: repoShortName(variables.repo), quantization: variables.variant || "default",
        operationId: result.operation_id, status: "running", repo: variables.repo, variant: variables.variant,
      });
      try {
        await pollDownload(result.operation_id, result.model_id);
      } catch (error) {
        notify(String(error));
      }
    },
    onError: error => { setPending(null); notify(String(error)); },
  });

  async function pollDownload(operationId: string, modelId: string) {
    for (let attempt = 0; attempt < 1440; attempt++) {
      await new Promise(resolve => setTimeout(resolve, 500));
      const op = await coreApi.operation(operationId);
      if (op.status === "succeeded") {
        setPending(null);
        setSelected(modelId);
        void invalidateModels();
        notify(t("downloadComplete"));
        return;
      }
      if (op.status === "failed") {
        // Keep the row so the user can inspect the error and retry from the detail pane.
        setPending(prev => prev ? { ...prev, status: "failed", error: op.error } : prev);
        return;
      }
      if (op.status === "cancelled") { setPending(null); return; }
      setPending(prev => prev ? { ...prev, status: op.status } : prev);
    }
    setPending(null);
  }

  // Poll a Core operation until it settles; used for model activation.
  async function pollOperation(operationId: string, onSuccess: () => void) {
    for (let attempt = 0; attempt < 720; attempt++) {
      await new Promise(resolve => setTimeout(resolve, 500));
      const op = await coreApi.operation(operationId);
      if (op.status === "succeeded") { onSuccess(); return; }
      if (op.status === "failed") { throw new Error(op.error || t("downloadFailed")); }
      if (op.status === "cancelled") { return; }
    }
  }

  function startDownload() {
    download.mutate({ repo: repo.trim(), variant: variant.trim() || undefined });
  }

  function retryDownload() {
    if (!pending) return;
    download.mutate({ repo: pending.repo, variant: pending.variant });
  }

  return <div className="split-view">
    <section className="list-pane">
      <div className="pane-title model-pane-title">
        <div><span className="feature-title-with-badge">{t("localModels")}<AdvancedBadge>{t("advancedPlanBadge")}</AdvancedBadge></span><small>{data.runtime.name}{data.runtime.provider ? ` · ${data.runtime.provider}` : ""}</small></div>
        <div className="pane-title__actions">
          <Button size="icon" variant="ghost" onClick={() => requireAdvanced(() => setManual(true))} aria-label={t("downloadModel")}><Plus size={14}/></Button>
          <Button size="icon" variant="ghost" onClick={() => scan.mutate()} aria-label={t("scanModels")}><RefreshCw size={14}/></Button>
        </div>
      </div>
      <div className="model-list">
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
        {pending && <button key={`pending-${pending.modelId}`} className={`model-pending ${pendingSelected ? "selected" : ""}`}
          title={pending.status === "failed" ? t("downloadFailed") : t("downloading")} onClick={() => setSelected(pending.modelId)}>
          <span className="model-icon"><Box size={16}/></span>
          <div>
            <strong>{pending.name}</strong>
            <code>{pending.modelId}</code>
            <small>{pending.status === "failed" ? t("downloadFailed") : t("downloading")}</small>
          </div>
          <StatusDot tone={pending.status === "failed" ? "error" : "warning"}/>
        </button>}
      </div>
    </section>
    <section className="detail-pane">
      {pendingSelected
        ? <PendingDetail pending={pending} retrying={download.isPending} onRetry={retryDownload}/>
        : current
          ? <ModelDetail model={current} runtimeAvailable={data.runtime.available}
              onActivate={model => requireAdvanced(() => activate.mutate(model.id), model.built_in)} onUnload={id => unload.mutate(id)}
              onDelete={id => setDeleteTarget(id)} activatePending={activate.isPending}/>
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
    <Dialog open={deleteTarget !== null} title={t("confirmDeleteModel")} onClose={() => setDeleteTarget(null)}
      footer={<>
        <Button onClick={() => setDeleteTarget(null)}>{t("cancel")}</Button>
        <Button variant="danger" onClick={() => deleteTarget && remove.mutate(deleteTarget)}>{t("confirm")}</Button>
      </>}>
      <p className="dialog-message">{t("deleteModel")}</p>
    </Dialog>
  </div>;
}

function PendingDetail({ pending, retrying, onRetry }: { pending: PendingDownload; retrying: boolean; onRetry: () => void }) {
  const { t } = useI18n();
  const failed = pending.status === "failed";
  const label = failed ? t("downloadFailed") : t("downloading");
  return <>
    <header className="detail-header">
      <div>
        <span>{t("downloadStatus")}</span>
        <h2>{pending.name}</h2>
        <code>{pending.modelId}</code>
      </div>
      <div className="header-actions">
        {failed && <Button variant="primary" icon={<RotateCw size={13}/>} disabled={retrying} onClick={onRetry}>{t("retry")}</Button>}
      </div>
    </header>
    <section className="detail-section detail-section--summary">
      <h3>{t("downloadStatus")}</h3>
      <dl className="property-grid">
        <div><dt>{t("status")}</dt><dd className={failed ? "error" : "success"} title={label}>{label}</dd></div>
        <div><dt>{t("quantization")}</dt><dd title={pending.quantization}>{pending.quantization}</dd></div>
      </dl>
      {failed && pending.error && <p className="validation-error">{pending.error}</p>}
    </section>
  </>;
}

function ModelDetail({ model, runtimeAvailable, onActivate, onUnload, onDelete, activatePending }: {
  model: ModelPackage;
  runtimeAvailable: boolean;
  onActivate: (model: ModelPackage) => void;
  onUnload: (id: string) => void;
  onDelete: (id: string) => void;
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
          : <>
              <Button variant="primary" disabled={!model.valid || !runtimeAvailable || activatePending} onClick={() => onActivate(model)}>{t("activate")}</Button>
              {!model.built_in && <Button variant="danger" icon={<Trash2 size={13}/>} onClick={() => onDelete(model.id)}>{t("deleteModel")}</Button>}
            </>}
      </div>
    </header>
    <section className="detail-section detail-section--summary">
      <h3>{t("modelSpecs")}</h3>
      <dl className="property-grid">
        <div><dt>{t("quantization")}</dt><dd title={manifest.quantization}>{manifest.quantization}</dd></div>
        <div><dt>{t("version")}</dt><dd><code title={manifest.version}>{manifest.version}</code></dd></div>
        <div><dt>{t("labelScheme")}</dt><dd title={manifest.label_scheme}>{manifest.label_scheme}</dd></div>
        <div><dt>{t("sequence")}</dt><dd title={`${manifest.max_tokens} / stride ${manifest.stride}`}>{manifest.max_tokens} / stride {manifest.stride}</dd></div>
      </dl>
    </section>
    <section className="detail-section">
      <h3>{t("validation")}</h3>
      {model.valid ? <div className="validation-ok"><CheckCircle2 size={16}/>{t("valid")}</div> : (model.errors ?? []).map(error => <p className="validation-error" key={error}>{error}</p>)}
    </section>
  </>;
}

function repoShortName(repo: string) {
  const cleaned = repo.trim().replace(/\/+$/, "");
  const parts = cleaned.split("/");
  return parts[parts.length - 1] || cleaned;
}
