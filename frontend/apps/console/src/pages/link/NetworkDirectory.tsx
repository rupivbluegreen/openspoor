import { useState } from 'react';
import { Button, Card, Input, List, Select, Space, Switch, Tag, Typography } from 'antd';
import { LinkOutlined, SafetyCertificateFilled } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { useTranslation } from 'react-i18next';
import { api, ModalForm, ProFormTextArea } from '@openspoor/ui/pro';
import { useSubmit } from '../../lib/hooks';
import type { Profile } from './linkTypes';

const KINDS = ['manufacturer', 'wholesaler', '3pl', 'pharmacy', 'hospital', 'retailer', 'distributor', 'grower', 'processor'];

/** Search published network profiles and ask to connect. */
export function NetworkDirectory() {
  const { t } = useTranslation();
  const [q, setQ] = useState('');
  const [kind, setKind] = useState<string>();
  const [verified, setVerified] = useState(false);
  const res = useQuery({
    queryKey: ['/partners/directory', q],
    queryFn: () => api.list<Profile>('/partners/directory', { q }),
  });
  const requests = useQuery({ queryKey: ['/partners/directory/requests'], queryFn: () => api.list<{ id: string; fromName: string; message: string; createdAt: string }>('/partners/directory/requests') });
  const connect = (id: string) => (v: { message?: string }) => api.post(`/partners/directory/${id}/connect`, v);
  const submit = useSubmit((v: { id: string; message?: string }) => connect(v.id)({ message: v.message }));
  const items = (res.data?.items ?? []).filter((p) => (!kind || p.kind === kind) && (!verified || p.verified));
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small">
        <Space wrap>
          <Input.Search placeholder={`${t('action.search')} (name, GLN, company prefix)`} enterButton onSearch={setQ} allowClear style={{ width: 380 }} />
          <Select allowClear placeholder={t('field.kind')} style={{ width: 160 }} value={kind} onChange={setKind} options={KINDS.map((k) => ({ value: k, label: k }))} />
          <Space>
            <Switch checked={verified} onChange={setVerified} size="small" />
            <Typography.Text>Verified only</Typography.Text>
          </Space>
        </Space>
      </Card>
      <List
        loading={res.isFetching}
        dataSource={items}
        renderItem={(p) => (
          <List.Item
            actions={[
              <ModalForm<{ message?: string }>
                key="connect"
                title={`Connect with ${p.name}`}
                trigger={<Button icon={<LinkOutlined />}>Request connection</Button>}
                modalProps={{ destroyOnHidden: true }}
                onFinish={(v) => submit({ id: p.tenantId, message: v.message })}
              >
                <ProFormTextArea name="message" label="Message" fieldProps={{ maxLength: 2000, rows: 4 }} />
              </ModalForm>,
            ]}
          >
            <List.Item.Meta
              title={
                <Space>
                  {p.name}
                  {p.verified && <Tag icon={<SafetyCertificateFilled />} color="success">verified</Tag>}
                  <Tag>{p.kind}</Tag>
                  {p.country && <Tag>{p.country}</Tag>}
                </Space>
              }
              description={
                <Space direction="vertical" size={2}>
                  {p.description && <Typography.Text>{p.description}</Typography.Text>}
                  {(p.glns?.length ?? 0) > 0 && <Typography.Text type="secondary">GLN {p.glns.join(', ')}</Typography.Text>}
                  {(p.companyPrefixes?.length ?? 0) > 0 && <Typography.Text type="secondary">Company prefix {p.companyPrefixes.join(', ')}</Typography.Text>}
                  <Space wrap>
                    {(p.licenses ?? []).map((l) => (
                      <Tag key={l.id ?? l.number} color={l.verified ? 'success' : 'default'}>
                        {l.kind} {l.number} · {l.authority}
                        {l.validTo ? ` · ${l.validTo}` : ''}
                        {l.verified ? ' ✓' : ''}
                      </Tag>
                    ))}
                  </Space>
                  {p.contact && <Typography.Text copyable>{p.contact}</Typography.Text>}
                </Space>
              }
            />
          </List.Item>
        )}
      />
      {(requests.data?.items.length ?? 0) > 0 && (
        <Card size="small" title="Connection requests">
          <List
            size="small"
            dataSource={requests.data?.items ?? []}
            renderItem={(r) => (
              <List.Item>
                <List.Item.Meta title={r.fromName} description={r.message || '–'} />
                <Typography.Text type="secondary">{dayjs(r.createdAt).format('YYYY-MM-DD')}</Typography.Text>
              </List.Item>
            )}
          />
        </Card>
      )}
    </Space>
  );
}
