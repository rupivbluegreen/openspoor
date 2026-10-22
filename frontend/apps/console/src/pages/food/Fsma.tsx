import { useState } from 'react';
import { Card, Table } from 'antd';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { api, downloadBlob, notifyError, PageContainer, ProForm, ProFormDateRangePicker, ProFormText } from '@openspoor/ui/pro';

/** Minimal RFC 4180 CSV parser (quoted fields, escaped quotes). */
export function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let q = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"';
        i++;
      } else if (c === '"') q = false;
      else field += c;
    } else if (c === '"') q = true;
    else if (c === ',') {
      row.push(field);
      field = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++;
      row.push(field);
      rows.push(row);
      row = [];
      field = '';
    } else field += c;
  }
  if (field !== '' || row.length) {
    row.push(field);
    rows.push(row);
  }
  return rows;
}

export default function Fsma() {
  const { t } = useTranslation();
  const [rows, setRows] = useState<string[][] | null>(null);
  return (
    <PageContainer title={t('nav.fsmaExport')} subTitle="FDA 24-hour electronic sortable spreadsheet">
      <Card style={{ marginBottom: 16 }}>
        <ProForm<{ range?: [string, string]; tlc?: string }>
          layout="inline"
          submitter={{ searchConfig: { submitText: `${t('action.export')} CSV` }, resetButtonProps: false }}
          onFinish={async (v) => {
            try {
              const from = v.range?.[0] ? dayjs(v.range[0]).format('YYYY-MM-DD') : undefined;
              const to = v.range?.[1] ? dayjs(v.range[1]).format('YYYY-MM-DD') : undefined;
              const csv = await api.get<string>('/food/fsma204/export', { from, to, tlc: v.tlc }, { responseType: 'text', headers: { Accept: 'text/csv' } });
              setRows(parseCsv(csv));
              downloadBlob(csv, `fsma204-${from ?? 'all'}-${to ?? 'now'}.csv`, 'text/csv;charset=utf-8');
            } catch (e) {
              notifyError(e);
            }
          }}
        >
          <ProFormDateRangePicker name="range" label="Period" />
          <ProFormText name="tlc" label="TLC" />
        </ProForm>
      </Card>
      {rows && rows.length > 0 && (
        <Card title={t('action.preview')}>
          <Table
            size="small"
            scroll={{ x: 'max-content' }}
            rowKey={(_r, i) => String(i)}
            dataSource={rows.slice(1).map((r) => Object.fromEntries(r.map((v, i) => [String(i), v])))}
            columns={rows[0].map((h, i) => ({ title: h, dataIndex: String(i) }))}
            pagination={{ pageSize: 25 }}
          />
        </Card>
      )}
    </PageContainer>
  );
}
