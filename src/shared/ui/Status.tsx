import { cva } from "class-variance-authority";
import { cn } from "../lib/utils";

const dot = cva("inline-block size-[7px] shrink-0 rounded-full", { variants: { tone: {
  success: "bg-emerald-600 shadow-[0_0_0_3px_#e2f0eb]", warning: "bg-amber-600 shadow-[0_0_0_3px_#f8ead8]", error: "bg-destructive", muted: "bg-zinc-400"
} }, defaultVariants: { tone: "muted" } });
const badge = cva("inline-flex w-max items-center rounded-md px-1.5 py-1 font-mono text-[8px] font-medium", { variants: { tone: {
  success: "bg-emerald-50 text-emerald-700", warning: "bg-amber-50 text-amber-700", error: "bg-destructive/10 text-destructive", neutral: "bg-muted text-muted-foreground"
} }, defaultVariants: { tone: "neutral" } });
export function StatusDot({ tone = "muted", className }: { tone?: "success" | "warning" | "error" | "muted"; className?: string }) { return <i className={cn(dot({tone}),className)} aria-hidden="true"/>; }
export function Badge({ tone = "neutral", children }: { tone?: "success" | "warning" | "error" | "neutral"; children: React.ReactNode }) { return <span className={cn(badge({tone}))}>{children}</span>; }
