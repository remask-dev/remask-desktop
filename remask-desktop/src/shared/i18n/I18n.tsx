import { createContext, useContext, useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { messages, type Locale, type MessageKey } from "./messages";
import { defaultLocale, localeConfig, localeStorageKey, resolveLocale } from "./locales";

type I18nValue = { locale: Locale; setLocale: (locale: Locale) => void; t: (key: MessageKey) => string; dateLocale: string };
const I18nContext = createContext<I18nValue | null>(null);

/**
 * Resolve a locale only from the locales the UI actually ships. An explicit
 * user choice always wins; otherwise the first visit defaults to English.
 */
function readStoredLocale(): Locale | null {
  if (typeof window === "undefined") return null;
  try {
    return resolveLocale(window.localStorage.getItem(localeStorageKey));
  } catch {
    return null;
  }
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, update] = useState<Locale>(() => readStoredLocale() ?? defaultLocale);
  useLayoutEffect(() => {
    document.documentElement.lang = localeConfig[locale].htmlLang;
  }, [locale]);
  const value = useMemo<I18nValue>(() => ({
    locale,
    setLocale(next) {
      if (next === locale) return;
      try { window.localStorage.setItem(localeStorageKey, next); } catch { /* Continue with an in-memory preference. */ }
      update(next);
    },
    t: (key) => messages[locale][key],
    dateLocale: localeConfig[locale].dateLocale,
  }), [locale]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}
export function useI18n() { const value = useContext(I18nContext); if (!value) throw new Error("I18nProvider missing"); return value; }
