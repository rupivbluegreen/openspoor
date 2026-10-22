import { Button, Dropdown, Tooltip } from 'antd';
import { MoonOutlined, SunOutlined, TranslationOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useAppSettings } from '../AppProvider';
import { LANGS } from '../i18n';

export function LanguageSwitch() {
  const { lang, setLang } = useAppSettings();
  const { t } = useTranslation();
  return (
    <Dropdown
      menu={{
        selectable: true,
        selectedKeys: [lang],
        items: LANGS.map((l) => ({ key: l.code, label: l.label })),
        onClick: ({ key }) => setLang(key as typeof lang),
      }}
    >
      <Button type="text" icon={<TranslationOutlined />} aria-label={t('common.language')}>
        {lang.toUpperCase()}
      </Button>
    </Dropdown>
  );
}

export function ThemeToggle() {
  const { mode, setMode } = useAppSettings();
  const { t } = useTranslation();
  const next = mode === 'dark' ? 'light' : 'dark';
  return (
    <Tooltip title={`${t('common.theme')}: ${t(`common.${next}`)}`}>
      <Button
        type="text"
        aria-label={t('common.theme')}
        icon={mode === 'dark' ? <SunOutlined /> : <MoonOutlined />}
        onClick={() => setMode(next)}
      />
    </Tooltip>
  );
}
