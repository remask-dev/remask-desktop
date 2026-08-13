import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppProvider } from "./app/AppContext";
import { Shell } from "./app/Shell";
import { ErrorBoundary } from "./app/ErrorBoundary";
import { I18nProvider } from "./shared/i18n/I18n";
import "./styles/app.css";

const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: 3_000, refetchOnWindowFocus: true } } });
createRoot(document.getElementById("root")!).render(<StrictMode><ErrorBoundary><QueryClientProvider client={queryClient}><I18nProvider><AppProvider><Shell/></AppProvider></I18nProvider></QueryClientProvider></ErrorBoundary></StrictMode>);
