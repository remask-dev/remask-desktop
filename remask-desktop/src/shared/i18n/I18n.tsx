import { createContext, useContext, useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { messages, type Locale, type MessageKey } from "./messages";

type I18nValue = { locale: Locale; setLocale: (locale: Locale) => void; t: (key: MessageKey) => string; dateLocale: string };
const I18nContext = createContext<I18nValue | null>(null);

const localeKey = "remask.language";

/**
 * Resolve a locale only from the locales the UI actually ships. An explicit
 * user choice always wins; otherwise the first visit follows the OS/browser
 * language and falls back to English for unsupported languages.
 */
function detectLocale(): Locale {
  if (typeof navigator === "undefined") return "en";
  const candidates = navigator.languages?.length ? navigator.languages : [navigator.language];
  for (const language of candidates) {
    const normalized = language?.toLowerCase() ?? "";
    if (normalized.startsWith("zh")) return "zh";
    if (normalized.startsWith("en")) return "en";
  }
  return "en";
}

function readStoredLocale(): Locale | null {
  if (typeof window === "undefined") return null;
  try {
    const stored = window.localStorage.getItem(localeKey);
    return stored === "zh" || stored === "en" ? stored : null;
  } catch {
    return null;
  }
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, update] = useState<Locale>(() => readStoredLocale() ?? detectLocale());
  useLayoutEffect(() => {
    document.documentElement.lang = locale === "zh" ? "zh-CN" : "en-US";
  }, [locale]);
  const value = useMemo<I18nValue>(() => ({
    locale,
    setLocale(next) {
      if (next === locale) return;
      try { window.localStorage.setItem(localeKey, next); } catch { /* Continue with an in-memory preference. */ }
      update(next);
    },
    t: (key) => messages[locale][key],
    dateLocale: locale === "zh" ? "zh-CN" : "en-US",
  }), [locale]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}
export function useI18n() { const value = useContext(I18nContext); if (!value) throw new Error("I18nProvider missing"); return value; }
