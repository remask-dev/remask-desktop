import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { messages, type Locale, type MessageKey } from "./messages";

type I18nValue = { locale: Locale; setLocale: (locale: Locale) => void; t: (key: MessageKey) => string; dateLocale: string };
const I18nContext = createContext<I18nValue | null>(null);

const localeKey = "remask.language";
export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, update] = useState<Locale>(() => (localStorage.getItem(localeKey) === "en" ? "en" : "zh"));
  useEffect(() => { document.documentElement.lang = locale === "zh" ? "zh-CN" : "en-US"; }, [locale]);
  const value = useMemo<I18nValue>(() => ({ locale, setLocale(next) { localStorage.setItem(localeKey, next); update(next); }, t: (key) => messages[locale][key], dateLocale: locale === "zh" ? "zh-CN" : "en-US" }), [locale]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}
export function useI18n() { const value = useContext(I18nContext); if (!value) throw new Error("I18nProvider missing"); return value; }
