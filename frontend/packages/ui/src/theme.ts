import { theme as antdTheme, type ThemeConfig } from 'antd';

/** Open Spoor design tokens. "Spoor" is Dutch for track/trace: a calm deep teal-green. */
export const tokens = {
  colorPrimary: '#0B6E63',
  colorInfo: '#1F6F8B',
  colorSuccess: '#2F8F46',
  colorWarning: '#C98A0B',
  colorError: '#C0392B',
  colorLink: '#0B6E63',
  borderRadius: 6,
  fontFamily:
    "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', 'Noto Sans', 'Noto Sans Arabic', Tahoma, Arial, sans-serif",
  fontFamilyCode: "'JetBrains Mono', 'SFMono-Regular', Menlo, Consolas, 'Liberation Mono', monospace",
} as const;

export type ThemeMode = 'light' | 'dark';

const HEX = /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i;
export function isHexColor(v: unknown): v is string {
  return typeof v === 'string' && HEX.test(v.trim());
}

export function buildTheme(mode: ThemeMode, brandColor?: string | null): ThemeConfig {
  const primary = isHexColor(brandColor) ? brandColor.trim() : tokens.colorPrimary;
  return {
    algorithm: mode === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    cssVar: { key: 'openspoor' },
    token: {
      ...tokens,
      colorPrimary: primary,
      colorLink: primary,
      colorBgLayout: mode === 'dark' ? '#0E1413' : '#F4F7F6',
    },
    components: {
      Layout: { headerBg: mode === 'dark' ? '#121A19' : '#FFFFFF' },
      Card: { borderRadiusLG: 8 },
      Table: { headerBg: mode === 'dark' ? '#16201F' : '#F0F5F4' },
    },
  };
}
