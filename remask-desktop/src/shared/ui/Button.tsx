import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { cn } from "../lib/utils";

export const buttonVariants = cva(
  "inline-flex h-8 shrink-0 items-center justify-center gap-1.5 rounded-md px-3 text-[10px] font-semibold transition-colors focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:shrink-0",
  { variants: { variant: {
    primary: "bg-primary text-primary-foreground shadow-sm hover:bg-primary/90",
    secondary: "border border-border bg-secondary text-secondary-foreground hover:bg-secondary/75",
    ghost: "bg-transparent text-primary hover:bg-accent hover:text-accent-foreground",
    danger: "bg-destructive/10 text-destructive hover:bg-destructive/15"
  }, size: { default: "h-8 px-3", icon: "size-8 px-0" } }, defaultVariants: { variant: "secondary", size: "default" } }
);

export function Button({ variant, size, icon, children, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants> & { icon?: ReactNode }) {
  return <button className={cn(buttonVariants({ variant, size }), className)} {...props}>{icon}{children}</button>;
}
