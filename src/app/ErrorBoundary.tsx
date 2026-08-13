import { Component, type ErrorInfo, type ReactNode } from "react";
import { ShieldAlert } from "lucide-react";
import { Button } from "../shared/ui/Button";

export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error("Remask UI error", error, info.componentStack); }
  render() {
    if (!this.state.error) return this.props.children;
    return <main className="grid h-screen place-content-center bg-background p-8 text-center"><div className="mx-auto mb-4 grid size-12 place-items-center rounded-xl bg-destructive/10 text-destructive"><ShieldAlert size={22}/></div><h1 className="text-base font-semibold">界面加载失败</h1><p className="mt-2 max-w-md text-[11px] leading-relaxed text-muted-foreground">{this.state.error.message}</p><Button className="mx-auto mt-5" variant="primary" onClick={()=>window.location.reload()}>重新加载</Button></main>;
  }
}
