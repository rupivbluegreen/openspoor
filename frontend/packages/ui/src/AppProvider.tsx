import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { App as AntdApp, ConfigProvider } from 'antd';
import type { Locale } from 'antd/es/locale';
import enUS from 'antd/locale/en_US';
import nlNL from 'antd/locale/nl_NL';
import arEG from 'antd/locale/ar_EG';
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import dayjs from 'dayjs';
import 'dayjs/locale/nl';
import 'dayjs/locale/ar';
import { buildTheme, type ThemeMode } from './theme';
import { dirOf, initI18n, LANG_KEY, type Lang } from './i18n';
import { bindNotifier, notifyError } from './notify';
import { safeStorage } from './storage';
import { ApiError } from './api/errors';

const antdLocales: Record<Lang, Locale> = { en: enUS, nl: nlNL, ar: arEG };
const MODE_KEY = 'openspoor.theme';

export interface AppSettings {
  mode: ThemeMode;
  setMode: (m: ThemeMode) => void;
  lang: Lang;
  setLang: (l: Lang) => void;
  direction: 'ltr' | 'rtl';
  brandColor: string | null;
  setBrandColor: (c: string | null) => void;
}

const SettingsContext = createContext<AppSettings | null>(null);

export function useAppSettings(): AppSettings {
  const ctx = useContext(SettingsContext);
  if (!ctx) throw new Error('useAppSettings must be used inside <AppProvider>');
  return ctx;
}

function initialMode(): ThemeMode {
  const saved = safeStorage.get(MODE_KEY);
  if (saved === 'light' || saved === 'dark') return saved;
  try {
    return globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  } catch {
    return 'light';
  }
}

/** Shared QueryClient: problem+json titles surface as notifications for mutations and failed loads. */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache({
      onError: (err, query) => {
        if (query.meta?.silent) return;
        if (err instanceof ApiError && err.status === 401) return;
        notifyError(err);
      },
    }),
    mutationCache: new MutationCache({
      onError: (err, _v, _c, mutation) => {
        if (mutation.meta?.silent) return;
        notifyError(err);
      },
    }),
    defaultOptions: {
      queries: {
        retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
        refetchOnWindowFocus: false,
        staleTime: 15_000,
      },
    },
  });
}

function NotifierBridge() {
  const { notification, message } = AntdApp.useApp();
  useEffect(() => bindNotifier(notification, message), [notification, message]);
  return null;
}

export interface AppProviderProps {
  children: ReactNode;
  queryClient?: QueryClient;
  /** Extra providers rendered inside ConfigProvider (e.g. ProConfigProvider from `@openspoor/ui/pro`). */
  wrapper?: (children: ReactNode, s: { mode: ThemeMode; lang: Lang }) => ReactNode;
}

export function AppProvider({ children, queryClient, wrapper }: AppProviderProps) {
  const i18n = useMemo(() => initI18n(), []);
  const [client] = useState(() => queryClient ?? createQueryClient());
  const [mode, setModeState] = useState<ThemeMode>(initialMode);
  const [lang, setLangState] = useState<Lang>(() => (i18n.language as Lang) || 'en');
  const [brandColor, setBrandColor] = useState<string | null>(null);
  const direction = dirOf(lang);

  const setMode = useCallback((m: ThemeMode) => {
    setModeState(m);
    safeStorage.set(MODE_KEY, m);
  }, []);
  const setLang = useCallback(
    (l: Lang) => {
      setLangState(l);
      safeStorage.set(LANG_KEY, l);
      void i18n.changeLanguage(l);
    },
    [i18n],
  );

  useEffect(() => {
    const el = document.documentElement;
    el.setAttribute('dir', direction);
    el.setAttribute('lang', lang);
    el.dataset.theme = mode;
    el.style.colorScheme = mode;
    dayjs.locale(lang);
  }, [direction, lang, mode]);

  const settings = useMemo<AppSettings>(
    () => ({ mode, setMode, lang, setLang, direction, brandColor, setBrandColor }),
    [mode, setMode, lang, setLang, direction, brandColor],
  );
  const theme = useMemo(() => buildTheme(mode, brandColor), [mode, brandColor]);

  return (
    <I18nextProvider i18n={i18n}>
      <SettingsContext.Provider value={settings}>
        <ConfigProvider theme={theme} locale={antdLocales[lang]} direction={direction}>
          {(wrapper ?? ((c) => c))(
            <AntdApp>
              <NotifierBridge />
              <QueryClientProvider client={client}>{children}</QueryClientProvider>
            </AntdApp>,
            { mode, lang },
          )}
        </ConfigProvider>
      </SettingsContext.Provider>
    </I18nextProvider>
  );
}
