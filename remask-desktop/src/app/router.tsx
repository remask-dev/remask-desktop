import { Navigate, createBrowserRouter } from "react-router-dom";
import { Shell } from "./Shell";
import { RouteErrorBoundary } from "./RouteErrorBoundary";

/**
 * Route modules are lazy-loaded so navigation is also a code-splitting
 * boundary. Each page owns its TanStack Query hooks and fetches only after its
 * route is mounted.
 */
export const router = createBrowserRouter([
  {
    element: <Shell />,
    errorElement: <RouteErrorBoundary />,
    children: [
      { index: true, element: <Navigate to="/overview" replace /> },
      { path: "overview", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/overview/Overview")).Overview }) },
      { path: "logs", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/logs/Logs")).Logs }) },
      { path: "test", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/test/LocalTest")).LocalTest }) },
      { path: "rules", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/rules/Rules")).Rules }) },
      { path: "services", element: <Navigate to="/gateway" replace /> },
      { path: "gateway", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/gateway/Gateway")).Gateway }) },
      { path: "models", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/models/Models")).Models }) },
      { path: "settings", errorElement: <RouteErrorBoundary />, lazy: async () => ({ Component: (await import("../features/settings/SettingsView")).SettingsView }) },
    ],
  },
]);
