import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
type AppValue = { toast: string; notify: (message: string) => void };
const Context = createContext<AppValue | null>(null);
export function AppProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState("");
  const value = useMemo(() => ({ toast, notify(message: string) { setToast(message); window.setTimeout(() => setToast(""), 2400); } }), [toast]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
export function useApp() { const value = useContext(Context); if (!value) throw new Error("AppProvider missing"); return value; }
