import { useEffect } from "react";
import { isRouteErrorResponse, useRouteError } from "react-router-dom";
import { writeClientLog } from "../shared/clientLogging";
import { PageState } from "../shared/ui/PageState";

function routeError(value: unknown): Error {
  if (value instanceof Error) return value;
  if (isRouteErrorResponse(value)) return new Error(`${value.status} ${value.statusText}`.trim());
  return new Error(typeof value === "string" ? value : "Page rendering failed");
}

/** Keeps route/render/chunk failures inside the current content area. */
export function RouteErrorBoundary() {
  const caught = useRouteError();
  const error = routeError(caught);

  useEffect(() => {
    writeClientLog("react", error, { source: window.location.pathname });
  }, [error.message, error.stack]);

  return <PageState pending={false} error={error} onRetry={() => window.location.reload()}/>;
}
