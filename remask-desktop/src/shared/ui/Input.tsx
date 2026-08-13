import type { InputHTMLAttributes } from "react";
import { cn } from "../lib/utils";

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn("h-8 w-full rounded-md border border-input bg-background px-2.5 text-[10px] transition-colors placeholder:text-muted-foreground/70 disabled:cursor-not-allowed disabled:opacity-50", className)} {...props}/>;
}
