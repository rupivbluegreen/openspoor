import { useState } from 'react';
import { Button, Card, Space, Table, Tag, Typography } from 'antd';
import { SaveOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import {
  api, CodeView, ModalForm, PageContainer, prettyJson, ProForm, ProFormDateTimeRangePicker, ProFormDigit, ProFormSelect, ProFormText,
  type EpcisEvent, type Query,
} from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import { BIZ_STEPS, cbv, DISPOSITIONS, EVENT_TYPES, extractEvents, locationId, nextPageToken } from '../../lib/epcis';

interface Form {
  eventType?: string[];
  time?: [string, string];
  EQ_bizStep?: string[];
  EQ_disposition?: string[];
  MATCH_anyEPC?: string;
  EQ_bizLocation?: string;
  perPage?: number;
}

function toQuery(v: Form): Query {
  return {
    eventType: v.eventType,
    GE_eventTime: v.time?.[0] ? dayjs(v.time[0]).toISOString() : undefined,
    LT_eventTime: v.time?.[1] ? dayjs(v.time[1]).toISOString() : undefined,
    EQ_bizStep: v.EQ_bizStep,
    EQ_disposition: v.EQ_disposition,
    MATCH_anyEPC: v.MATCH_anyEPC,
    EQ_bizLocation: v.EQ_bizLocation,
    perPage: v.perPage,
  };
}

export default function EventQuery() {
  const { t } = useTranslation();
  const [query, setQuery] = useState<Query | null>(null);
  const [tokens, setTokens] = useState<string[]>([]);
  const page = tokens[tokens.length - 1];
  const q = useQuery({
    queryKey: ['/epcis/events', query, page],
    queryFn: () => api.get<unknown>('/epcis/events', { ...query, nextPageToken: page }),
    enabled: !!query,
  });
  const events = extractEvents(q.data);
  const next = nextPageToken(q.data);
  const save = useSubmit((v: { name: string }) => api.post('/epcis/queries', { name: v.name, query }), { invalidate: [['/epcis/queries']] });
  const named = useQuery({ queryKey: ['/epcis/queries'], queryFn: () => api.list<{ name: string }>('/epcis/queries'), meta: { silent: true } });

  return (
    <PageContainer title={t('nav.eventQuery')} subTitle="EPCIS 2.0 query interface">
      <Card style={{ marginBottom: 16 }}>
        <ProForm<Form>
          layout="vertical"
          grid
          rowProps={{ gutter: 16 }}
          initialValues={{ perPage: 50 }}
          submitter={{ searchConfig: { submitText: t('action.query') } }}
          onFinish={async (v) => {
            setTokens([]);
            setQuery(toQuery(v));
          }}
          onReset={() => {
            setQuery(null);
            setTokens([]);
          }}
        >
          <ProFormSelect name="eventType" label={t('field.eventType')} mode="multiple" options={EVENT_TYPES} colProps={{ md: 8 }} />
          <ProFormDateTimeRangePicker name="time" label={t('field.eventTime')} colProps={{ md: 8 }} />
          <ProFormDigit name="perPage" label="Per page" min={1} max={1000} colProps={{ md: 8 }} />
          <ProFormSelect name="EQ_bizStep" label={t('field.bizStep')} mode="tags" options={BIZ_STEPS} colProps={{ md: 8 }} />
          <ProFormSelect name="EQ_disposition" label={t('field.disposition')} mode="tags" options={DISPOSITIONS} colProps={{ md: 8 }} />
          <ProFormText name="EQ_bizLocation" label={t('field.bizLocation')} placeholder="urn:epc:id:sgln:…" colProps={{ md: 8 }} />
          <ProFormText name="MATCH_anyEPC" label="MATCH_anyEPC" placeholder="urn:epc:id:sgtin:0614141.107346.*" colProps={{ md: 24 }} />
        </ProForm>
        {!!named.data?.items.length && (
          <Space wrap>
            <Typography.Text type="secondary">Named queries:</Typography.Text>
            {named.data.items.map((n) => (
              <Tag key={n.name}>{n.name}</Tag>
            ))}
          </Space>
        )}
      </Card>
      {query && (
        <Card
          title={`${t('field.events')} (${events.length})`}
          extra={
            <Space>
              <ModalForm<{ name: string }> title="Save as named query" trigger={<Button icon={<SaveOutlined />}>{t('action.save')}</Button>} onFinish={save} modalProps={{ destroyOnHidden: true }}>
                <ProFormText name="name" label={t('field.name')} rules={[{ required: true, pattern: /^[A-Za-z0-9_-]+$/ }]} />
              </ModalForm>
              <Button disabled={!tokens.length} onClick={() => setTokens((x) => x.slice(0, -1))}>‹</Button>
              <Button disabled={!next} onClick={() => next && setTokens((x) => [...x, next])}>›</Button>
            </Space>
          }
        >
          <Table<EpcisEvent>
            size="small"
            loading={q.isFetching}
            rowKey={(r) => String(r.eventID ?? `${r.eventTime}-${r.type}-${(r.epcList ?? []).join()}`)}
            dataSource={events}
            pagination={false}
            scroll={{ x: 'max-content' }}
            expandable={{ expandedRowRender: (r) => <CodeView code={prettyJson(r)} maxHeight={320} /> }}
            columns={[
              { title: t('field.eventTime'), dataIndex: 'eventTime', render: (v) => (v ? dayjs(v as string).format('YYYY-MM-DD HH:mm:ss') : '–') },
              { title: t('field.type'), dataIndex: 'type' },
              { title: t('field.action'), dataIndex: 'action' },
              { title: t('field.bizStep'), dataIndex: 'bizStep', render: (v) => cbv(v) },
              { title: t('field.disposition'), dataIndex: 'disposition', render: (v) => cbv(v) },
              { title: t('field.bizLocation'), render: (_v, r) => locationId(r.bizLocation) ?? '–' },
              { title: 'EPCs', render: (_v, r) => (r.epcList?.length ?? 0) + (r.childEPCs?.length ?? 0) },
            ]}
          />
        </Card>
      )}
    </PageContainer>
  );
}
