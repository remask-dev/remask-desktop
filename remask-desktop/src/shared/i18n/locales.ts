import type { Locale } from "./messages";

/**
 * Metadata for every locale shipped by the UI.
 *
 * Keep locale-specific values here so components never need to branch on a
 * language code. Adding a language means adding one entry to this table and
 * its messages; the rest of the UI consumes the same typed metadata.
 */
export const localeConfig = {
  en: { htmlLang: "en-US", dateLocale: "en-US", displayName: "English" },
  zh: { htmlLang: "zh-CN", dateLocale: "zh-CN", displayName: "简体中文" },
  ja: { htmlLang: "ja-JP", dateLocale: "ja-JP", displayName: "日本語" },
  de: { htmlLang: "de-DE", dateLocale: "de-DE", displayName: "Deutsch" },
} as const satisfies Record<Locale, { htmlLang: string; dateLocale: string; displayName: string }>;

export const defaultLocale: Locale = "en";
export const localeStorageKey = "remask.language";
export const localeOptions = (Object.keys(localeConfig) as Locale[]).map(locale => ({
  value: locale,
  label: localeConfig[locale].displayName,
}));

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && Object.prototype.hasOwnProperty.call(localeConfig, value);
}

export function resolveLocale(value: unknown): Locale | null {
  return isLocale(value) ? value : null;
}
