import { createContext, useCallback, useContext, useState, type ReactNode } from 'react';
import { Button, Dropdown, Flex, Input, Modal, Typography } from 'antd';
import { DeleteOutlined, DownloadOutlined, EyeOutlined, SaveOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { ColumnsState } from '../pro';
import { safeStorage } from '../storage';
import { notifyInfo, notifySuccess } from '../notify';

export interface SavedView {
  /** Search/filter form values. */
  params: Record<string, unknown>;
  /** ProTable column state (visibility, order, pinning). */
  columns?: Record<string, ColumnsState>;
  savedAt: string;
}

const ScopeCtx = createContext<string>('anon');

/** Scopes saved table views to a user (and tenant): wrap the app shell with the signed-in identity. */
export function TableViewsScope({ scope, children }: { scope?: string; children: ReactNode }) {
  return <ScopeCtx.Provider value={scope || 'anon'}>{children}</ScopeCtx.Provider>;
}

function read(key: string): Record<string, SavedView> {
  try {
    const v: unknown = JSON.parse(safeStorage.get(key) ?? '{}');
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, SavedView>) : {};
  } catch {
    return {};
  }
}

/** Named views of a table, persisted per user in localStorage under the table id. */
export function useSavedViews(tableId: string) {
  const scope = useContext(ScopeCtx);
  const key = `openspoor.views.${scope}.${tableId}`;
  const [, bump] = useState(0);
  const views = read(key);
  const write = useCallback(
    (next: Record<string, SavedView>) => {
      safeStorage.set(key, Object.keys(next).length ? JSON.stringify(next) : null);
      bump((n) => n + 1);
    },
    [key],
  );
  return {
    views,
    save: (name: string, view: Omit<SavedView, 'savedAt'>) => write({ ...read(key), [name]: { ...view, savedAt: new Date().toISOString() } }),
    remove: (name: string) => {
      const next = read(key);
      delete next[name];
      write(next);
    },
  };
}

export function ViewsDropdown({
  tableId,
  current,
  onApply,
}: {
  tableId: string;
  current: () => Omit<SavedView, 'savedAt'>;
  onApply: (v: SavedView) => void;
}) {
  const { t } = useTranslation();
  const { views, save, remove } = useSavedViews(tableId);
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState('');
  const names = Object.keys(views).sort((a, b) => a.localeCompare(b));

  const commit = () => {
    const n = name.trim();
    if (!n) return;
    save(n, current());
    setNaming(false);
    setName('');
    notifySuccess(t('table.viewSaved'));
  };

  return (
    <>
      <Dropdown
        trigger={['click']}
        menu={{
          items: [
            ...(names.length
              ? names.map((n) => ({
                  key: `v:${n}`,
                  label: (
                    <Flex justify="space-between" align="center" gap={16}>
                      <Typography.Text ellipsis style={{ maxWidth: 220 }}>{n}</Typography.Text>
                      <Button
                        type="text"
                        size="small"
                        danger
                        icon={<DeleteOutlined />}
                        aria-label={`${t('table.deleteView')}: ${n}`}
                        title={t('table.deleteView')}
                        onClick={(e) => {
                          e.stopPropagation();
                          remove(n);
                        }}
                      />
                    </Flex>
                  ),
                  onClick: () => onApply(views[n]!),
                }))
              : [{ key: 'none', disabled: true, label: t('table.noViews') }]),
            { type: 'divider' as const },
            { key: 'save', icon: <SaveOutlined />, label: t('table.saveView'), onClick: () => setNaming(true) },
          ],
        }}
      >
        <Button icon={<EyeOutlined />}>{t('table.views')}</Button>
      </Dropdown>
      <Modal
        open={naming}
        title={t('table.saveView')}
        okText={t('action.save')}
        cancelText={t('action.cancel')}
        okButtonProps={{ disabled: !name.trim() }}
        onOk={commit}
        onCancel={() => setNaming(false)}
        destroyOnHidden
      >
        <Input autoFocus maxLength={60} placeholder={t('table.viewName')} aria-label={t('table.viewName')} value={name} onChange={(e) => setName(e.target.value)} onPressEnter={commit} />
      </Modal>
    </>
  );
}

export function ExportCsvButton({ onExport, count }: { onExport: () => void; count: number }) {
  const { t } = useTranslation();
  return (
    <Button icon={<DownloadOutlined />} onClick={() => (count ? onExport() : notifyInfo(t('table.nothingToExport')))}>
      {t('table.exportCsv')}
    </Button>
  );
}
