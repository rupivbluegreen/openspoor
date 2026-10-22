import { theme } from 'antd';

/** Open Spoor mark: a trail of footprints converging on a track line. */
export function Logo({ size = 28 }: { size?: number }) {
  const { token } = theme.useToken();
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-label="Open Spoor" role="img">
      <rect width="32" height="32" rx="8" fill={token.colorPrimary} />
      <path d="M6 24 C 12 22, 14 10, 26 8" stroke="#fff" strokeWidth="2.2" fill="none" strokeLinecap="round" strokeDasharray="0.1 4.4" />
      <circle cx="26" cy="8" r="3" fill="#fff" />
    </svg>
  );
}
