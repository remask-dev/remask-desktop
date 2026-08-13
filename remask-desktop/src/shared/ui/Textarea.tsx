import type { TextareaHTMLAttributes } from "react";
import { cn } from "../lib/utils";

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={cn("w-full resize-none rounded-md border border-input bg-background px-2.5 py-2 font-mono text-[9px] leading-relaxed transition-colors placeholder:text-muted-foreground/70", className)} {...props}/>;
}
