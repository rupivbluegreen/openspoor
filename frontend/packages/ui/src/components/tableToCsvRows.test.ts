import { describe, expect, it } from 'vitest';
import { tableToCsvRows, type ExportValue } from './QueryTable';
import type { ProColumns } from '@ant-design/pro-components';

type Row = { id: string; partnerId: string; items: unknown[]; note: string };

describe('tableToCsvRows', () => {
  const names: Record<string, string> = { p1: 'Harbor Distribution' };
  const columns: (ProColumns<Row> & ExportValue<Row>)[] = [
    { title: 'Partner', dataIndex: 'partnerId', exportValue: (r) => names[r.partnerId] ?? r.partnerId },
    { title: 'Items', key: 'items', exportValue: (r) => r.items.length },
    { title: 'Note', dataIndex: 'note' },
    { title: 'Hidden', dataIndex: 'id', hideInTable: true },
    { title: 'Actions', valueType: 'option' },
  ];
  const rows: Row[] = [{ id: 'x', partnerId: 'p1', items: [1, 2], note: 'ok' }];

  it('uses exportValue, keeps computed columns, drops hidden and action columns', () => {
    expect(tableToCsvRows(columns, rows)).toEqual({ header: ['Partner', 'Items', 'Note'], body: [['Harbor Distribution', 2, 'ok']] });
  });

  it('follows the column settings for order and visibility', () => {
    const state = { note: { order: 0 }, partnerId: { show: false } };
    expect(tableToCsvRows(columns, rows, state).header).toEqual(['Note', 'Items']);
  });
});
