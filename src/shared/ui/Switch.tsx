import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cn } from "../lib/utils";

export function Switch({ checked, onCheckedChange, className, disabled, ariaLabel }: { checked: boolean; onCheckedChange: (checked:boolean)=>void; className?: string; disabled?: boolean; ariaLabel?: string }) {
  return <SwitchPrimitive.Root aria-label={ariaLabel} disabled={disabled} checked={checked} onCheckedChange={onCheckedChange} className={cn("switch-control",className)}><SwitchPrimitive.Thumb className="switch-control__thumb"/></SwitchPrimitive.Root>;
}
