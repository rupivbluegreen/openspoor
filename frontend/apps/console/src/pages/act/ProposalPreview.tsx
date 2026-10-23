import type { ReactNode } from 'react';
import { Alert, List, Space, Table, Tag, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { CodeView, type AgentAction } from '@openspoor/ui/pro';

type Obj = Record<string, unknown>;
const obj = (v: unknown): Obj => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Obj) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const text = (v: unknown) => (typeof v === 'string' ? v : JSON.stringify(v));

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 12 }}>
      <Typography.Text strong>{title}</Typography.Text>
      <div style={{ marginTop: 4 }}>{children}</div>
    </div>
  );
}

/** mapping.save: the proposed field mapping and the regression cases stored with it. */
function MappingPreview({ a }: { a: AgentAction }) {
  const { t } = useTranslation();
  const input = obj(a.input);
  const fields = arr(obj(input.mapping).fields).map(obj);
  const cases = arr(input.cases).map(obj);
  return (
    <>
      <Section title={t('agents.fieldMapping')}>
        <Table
          size="small"
          rowKey="target"
          pagination={false}
          dataSource={fields}
          columns={[
            { title: t('agents.target'), dataIndex: 'target', render: (v) => <Typography.Text code>{text(v)}</Typography.Text> },
            { title: t('agents.source'), dataIndex: 'source', render: (v) => (v ? <Typography.Text code>{text(v)}</Typography.Text> : '–') },
          ]}
        />
      </Section>
      <Section title={`${t('agents.regressionCases')} (${cases.length})`}>
        <Table
          size="small"
          rowKey={(_r, i) => String(i)}
          pagination={false}
          dataSource={cases}
          columns={[
            { title: t('agents.sampleRow'), dataIndex: 'row', render: (v) => <Typography.Text type="secondary">{text(v)}</Typography.Text> },
            { title: t('agents.expectedValue'), dataIndex: 'expected', render: (v) => <Typography.Text code>{text(v)}</Typography.Text> },
          ]}
        />
      </Section>
    </>
  );
}

/** A case proposed by reg-watch: the diff excerpt and the products in the affected market. */
function ChangePreview({ a }: { a: AgentAction }) {
  const { t } = useTranslation();
  const after = obj(a.after);
  const products = arr(after.products).map(String);
  return (
    <>
      <Section title={t('agents.diffExcerpt')}>
        <CodeView code={String(after.diff)} maxHeight={280} />
      </Section>
      <Section title={`${t('agents.affectedProducts')}${after.market ? ` (${text(after.market)})` : ''}`}>
        {products.length ? (
          <Space wrap>{products.map((p) => <Tag key={p}>{p}</Tag>)}</Space>
        ) : (
          <Typography.Text type="secondary">{t('agents.noProducts')}</Typography.Text>
        )}
      </Section>
    </>
  );
}

/** investigation.assemble_answer: scope and what is still missing. The person signs; the agent never closes anything. */
function AnswerPreview({ a }: { a: AgentAction }) {
  const { t } = useTranslation();
  const after = obj(a.after);
  const missing = arr(after.missing).map(String);
  return (
    <>
      <Section title={t('agents.identifiers')}>
        <Typography.Text>{text(after.identifiers ?? 0)}</Typography.Text>
        {Number(after.notFound) > 0 && <Tag color="warning" style={{ marginInlineStart: 8 }}>{t('agents.notFound')}: {text(after.notFound)}</Tag>}
      </Section>
      <Section title={t('agents.missing')}>
        <List size="small" bordered dataSource={missing} renderItem={(m) => <List.Item>{m}</List.Item>} />
      </Section>
    </>
  );
}

/** Kind-specific preview of a proposal; other kinds fall back to the generic before/after diff. */
export function ProposalPreview({ action, fallback }: { action: AgentAction; fallback: ReactNode }) {
  const { t } = useTranslation();
  const after = obj(action.after);
  let body: ReactNode = fallback;
  if (action.kind === 'mapping.save') body = <MappingPreview a={action} />;
  else if (action.kind === 'investigation.assemble_answer') body = <AnswerPreview a={action} />;
  else if (typeof after.diff === 'string' && after.diff) body = <ChangePreview a={action} />;
  else if (action.kind === 'inspection.assemble') body = <CodeView code={JSON.stringify(action.input ?? {}, null, 2)} maxHeight={200} />;
  return (
    <>
      {action.autonomy === 'suggest' && <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={t('agents.suggestOnly')} />}
      {action.result?.auto === true && <Alert type="success" showIcon style={{ marginBottom: 12 }} message={t('agents.autoApplied')} />}
      {body}
    </>
  );
}
