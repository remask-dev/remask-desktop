import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router-dom";
import { AppProvider } from "./app/AppContext";
import { ErrorBoundary } from "./app/ErrorBoundary";
import { router } from "./app/router";
import { I18nProvider } from "./shared/i18n/I18n";
import { installGlobalClientLogging, writeClientLog } from "./shared/clientLogging";
import "./styles/app.css";

const platform = /Windows/i.test(navigator.userAgent) ? "windows" : /Macintosh|Mac OS X/i.test(navigator.userAgent) ? "macos" : "other";
document.documentElement.dataset.platform = platform;
document.addEventListener("contextmenu", (event) => event.preventDefault());
installGlobalClientLogging();
writeClientLog("lifecycle", "Client started");

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: (error, query) => {
    // An unavailable local Core is a normal lifecycle state. The version
    // query is the liveness probe, so its failures should not flood logs.
    if (query.queryKey[0] === "core" && query.queryKey[1] === "version") return;
    writeClientLog("query", error);
  } }),
  mutationCache: new MutationCache({ onError: error => writeClientLog("mutation", error) }),
  defaultOptions: {
    queries: { staleTime: 3_000, refetchOnWindowFocus: true, retry: 1, throwOnError: false },
    mutations: { throwOnError: false },
  },
});
createRoot(document.getElementById("root")!).render(<StrictMode><ErrorBoundary><QueryClientProvider client={queryClient}><I18nProvider><AppProvider><RouterProvider router={router}/></AppProvider></I18nProvider></QueryClientProvider></ErrorBoundary></StrictMode>);
