import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
export type View = "overview" | "logs" | "test" | "services" | "models" | "rules" | "settings";
type AppValue = { view: View; setView: (view: View) => void; toast: string; notify: (message: string) => void };
const Context = createContext<AppValue | null>(null);
export function AppProvider({ children }: { children: ReactNode }) {
  const [view, setView] = useState<View>("overview"); const [toast, setToast] = useState("");
  const value = useMemo(() => ({ view, setView, toast, notify(message: string) { setToast(message); window.setTimeout(() => setToast(""), 2400); } }), [view, toast]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useApp() { const value = useContext(Context); if (!value) throw new Error("AppProvider missing"); return value; }
