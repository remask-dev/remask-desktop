import { invoke } from "@tauri-apps/api/core";

type ClientLogEntry = {
  timestamp: string;
  kind: "error" | "unhandledrejection" | "react" | "query" | "mutation" | "lifecycle";
  message: string;
  stack?: string;
  component_stack?: string;
  source?: string;
};

const MAX_FIELD_LENGTH = 8_000;

function limit(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  const text = typeof value === "string" ? value : String(value);
  return text.length > MAX_FIELD_LENGTH ? `${text.slice(0, MAX_FIELD_LENGTH)}…` : text;
}

function redact(value: string | undefined): string | undefined {
  if (!value) return value;
  return value
    .replace(/(authorization\s*[:=]\s*bearer\s+)[^\s,;]+/gi, "$1[REDACTED]")
    .replace(/((?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,;]+/gi, "$1[REDACTED]")
    .replace(/\b(sk-[A-Za-z0-9_-]+|Bearer\s+[A-Za-z0-9._~-]+)/g, "[REDACTED]");
}

function errorDetails(value: unknown): Pick<ClientLogEntry, "message" | "stack"> {
  if (value instanceof Error) {
    return { message: redact(limit(value.message)) || value.name, stack: redact(limit(value.stack)) };
  }
  if (typeof value === "string") return { message: redact(limit(value)) || "Unknown error" };
  try {
    return { message: redact(limit(JSON.stringify(value))) || "Unknown error" };
  } catch {
    return { message: "Unknown error" };
  }
}

export function writeClientLog(kind: ClientLogEntry["kind"], error: unknown, extra?: { componentStack?: string; source?: string }) {
  const details = errorDetails(error);
  const entry: ClientLogEntry = {
    timestamp: new Date().toISOString(),
    kind,
    message: details.message,
    stack: details.stack,
    component_stack: redact(limit(extra?.componentStack)),
    source: redact(limit(extra?.source || `${window.location.origin}${window.location.pathname}`)),
  };

  // The logger must never become another source of UI failures. In browser
  // development mode this falls back to the console because no Tauri command
  // is available.
  try {
    void invoke("append_client_log", { entry: JSON.stringify(entry) }).catch(() => {
      if (kind !== "lifecycle") console.error("Remask client log", entry);
    });
  } catch {
    if (kind !== "lifecycle") console.error("Remask client log", entry);
  }
}

export function installGlobalClientLogging() {
  window.addEventListener("error", (event) => {
    writeClientLog("error", event.error || event.message, {
      source: event.filename ? `${event.filename}:${event.lineno}:${event.colno}` : undefined,
    });
  });
  window.addEventListener("unhandledrejection", (event) => {
    writeClientLog("unhandledrejection", event.reason);
  });
}
