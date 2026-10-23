import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { AutoComplete, Flex, Input, Typography, type InputRef } from 'antd';
import {
  AppstoreOutlined, BarcodeOutlined, FileSearchOutlined, SearchOutlined, SolutionOutlined, TagsOutlined, TeamOutlined, TruckOutlined,
} from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api, type SearchHit, type SearchResponse } from '@openspoor/ui/pro';

const KINDS = ['serial', 'product', 'shipment', 'partner', 'lot', 'case'] as const;
const ICONS: Record<string, ReactNode> = {
  serial: <BarcodeOutlined />,
  product: <AppstoreOutlined />,
  shipment: <TruckOutlined />,
  partner: <TeamOutlined />,
  lot: <TagsOutlined />,
  case: <SolutionOutlined />,
};

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

function isTyping(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  return el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) || el.getAttribute('role') === 'combobox';
}

/** Header search box: serials (scanned/typed GS1 codes), products, shipments, partners, lots and cases. */
export function GlobalSearch({ compact }: { compact?: boolean }) {
  const { t } = useTranslation();
  const nav = useNavigate();
  const inputRef = useRef<InputRef>(null);
  const [text, setText] = useState('');
  const q = useDebounced(text.trim(), 250);

  // "/" focuses the box unless the user is already typing somewhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || isTyping(e.target)) return;
      e.preventDefault();
      inputRef.current?.focus();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const res = useQuery({
    queryKey: ['search', q],
    queryFn: ({ signal }) => api.get<SearchResponse>('/search', { q }, { signal }),
    enabled: q.length >= 2,
    staleTime: 30_000,
    meta: { silent: true },
  });

  const hits = useMemo(() => (q.length >= 2 ? (res.data?.items ?? []) : []), [q, res.data]);
  const byValue = useMemo(() => new Map(hits.map((h) => [`${h.kind}:${h.id}`, h])), [hits]);
  const options = useMemo(() => {
    const groups = new Map<string, SearchHit[]>();
    for (const h of hits) groups.set(h.kind, [...(groups.get(h.kind) ?? []), h]);
    const order = [...KINDS, ...[...groups.keys()].filter((k) => !(KINDS as readonly string[]).includes(k))];
    return order
      .filter((k) => groups.has(k))
      .map((k) => ({
        label: t(`search.kind.${k}`, { defaultValue: k }),
        options: groups.get(k)!.map((h) => ({
          value: `${h.kind}:${h.id}`,
          label: (
            <Flex gap={8} align="center" style={{ minWidth: 0 }}>
              <span aria-hidden>{ICONS[h.kind] ?? <FileSearchOutlined />}</span>
              <Flex vertical style={{ minWidth: 0 }}>
                <Typography.Text ellipsis>{h.title}</Typography.Text>
                {h.subtitle ? (
                  <Typography.Text type="secondary" ellipsis style={{ fontSize: 12 }}>
                    {h.subtitle}
                  </Typography.Text>
                ) : null}
              </Flex>
            </Flex>
          ),
        })),
      }));
  }, [hits, t]);

  return (
    <AutoComplete
      value={text}
      options={options}
      onSearch={setText}
      onSelect={(v: string) => {
        const hit = byValue.get(v);
        setText('');
        if (hit) {
          inputRef.current?.blur();
          nav(hit.path);
        }
      }}
      defaultActiveFirstOption
      popupMatchSelectWidth={false}
      styles={{ root: { width: compact ? 'min(44vw, 180px)' : 'clamp(180px, 22vw, 320px)' }, popup: { root: { width: 'min(380px, calc(100vw - 32px))' } } }}
      notFoundContent={q.length >= 2 && !res.isFetching ? t('search.empty') : null}
    >
      <Input
        ref={inputRef}
        size="small"
        allowClear
        prefix={<SearchOutlined />}
        placeholder={compact ? t('action.search') : t('search.placeholder')}
        aria-label={`${t('action.search')} (${t('search.shortcut')})`}
        title={t('search.shortcut')}
        suffix={!compact && !text ? <Typography.Text keyboard type="secondary">/</Typography.Text> : undefined}
      />
    </AutoComplete>
  );
}
