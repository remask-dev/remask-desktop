import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { useI18n } from "../i18n/I18n";
export function Dialog({ open, title, description, children, footer, onClose }: { open: boolean; title: string; description?: string; children: ReactNode; footer?: ReactNode; onClose: () => void }) {
  const { t } = useI18n();
  return <DialogPrimitive.Root open={open} onOpenChange={(next) => !next && onClose()}><DialogPrimitive.Portal>
    <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-foreground/25 backdrop-blur-[2px] data-[state=closed]:animate-out data-[state=open]:animate-in"/>
    <DialogPrimitive.Content className="fixed left-1/2 top-1/2 z-50 grid w-[min(480px,calc(100vw-60px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-2xl focus:outline-none">
      <header className="flex items-start justify-between gap-4 border-b px-4 py-3.5"><div><DialogPrimitive.Title className="text-[13px] font-semibold tracking-tight">{title}</DialogPrimitive.Title>{description&&<DialogPrimitive.Description className="mt-1 text-[9px] leading-relaxed text-muted-foreground">{description}</DialogPrimitive.Description>}</div><DialogPrimitive.Close className="grid size-8 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground" aria-label={t("close")}><X size={16}/></DialogPrimitive.Close></header>
      <div className="max-h-[calc(100vh-180px)] overflow-auto px-4 py-3.5">{children}</div>
      {footer&&<footer className="flex items-center justify-end gap-2 border-t bg-muted/30 px-4 py-3">{footer}</footer>}
    </DialogPrimitive.Content>
  </DialogPrimitive.Portal></DialogPrimitive.Root>;
}
