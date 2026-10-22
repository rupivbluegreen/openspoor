import i18next, { type i18n as I18n } from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './en';
import nl from './nl';
import ar from './ar';
import { safeStorage } from '../storage';

export type Lang = 'en' | 'nl' | 'ar';
export const LANGS: { code: Lang; label: string; dir: 'ltr' | 'rtl' }[] = [
  { code: 'en', label: 'English', dir: 'ltr' },
  { code: 'nl', label: 'Nederlands', dir: 'ltr' },
  { code: 'ar', label: 'العربية', dir: 'rtl' },
];
export const resources = { en: { translation: en }, nl: { translation: nl }, ar: { translation: ar } } as const;
export const LANG_KEY = 'openspoor.lang';

export function isLang(v: unknown): v is Lang {
  return v === 'en' || v === 'nl' || v === 'ar';
}

export function dirOf(lang: Lang): 'ltr' | 'rtl' {
  return lang === 'ar' ? 'rtl' : 'ltr';
}

export function initialLang(): Lang {
  const saved = safeStorage.get(LANG_KEY);
  if (isLang(saved)) return saved;
  const nav = typeof navigator !== 'undefined' ? navigator.language?.slice(0, 2) : undefined;
  return isLang(nav) ? nav : 'en';
}

let instance: I18n | null = null;

/** Initialises the shared i18next instance once (synchronous; resources are bundled). */
export function initI18n(lang: Lang = initialLang()): I18n {
  if (instance) return instance;
  instance = i18next.createInstance();
  void instance.use(initReactI18next).init({
    resources,
    lng: lang,
    fallbackLng: 'en',
    interpolation: { escapeValue: false },
    initAsync: false,
    returnNull: false,
  });
  return instance;
}

/** Flattens nested messages into dotted keys (used by tests and tooling). */
export function flattenKeys(obj: Record<string, unknown>, prefix = ''): string[] {
  return Object.entries(obj).flatMap(([k, v]) =>
    v && typeof v === 'object' ? flattenKeys(v as Record<string, unknown>, `${prefix}${k}.`) : [`${prefix}${k}`],
  );
}

export { en, nl, ar };
