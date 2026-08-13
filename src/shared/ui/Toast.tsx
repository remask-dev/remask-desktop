import { cn } from "../lib/utils";
export function Toast({ message }: { message: string }) { return <div className={cn("pointer-events-none fixed bottom-9 left-1/2 z-[80] max-w-sm -translate-x-1/2 translate-y-1.5 rounded-lg border border-white/10 bg-zinc-900 px-3 py-2 text-[10px] text-white opacity-0 shadow-xl transition-all",message&&"translate-y-0 opacity-100")} role="status">{message}</div>; }
