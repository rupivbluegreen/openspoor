import { useState } from 'react';
import { Alert, Button, Card, Checkbox, Descriptions, Drawer, Empty, Input, InputNumber, List, Progress, Select, Space, Switch, Tabs, Tag, Typography } from 'antd';
import { ExperimentOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import dayjs from 'dayjs';
import { api, StatusTag, type Partner } from '@openspoor/ui/pro';
import { useAction } from '../../lib/hooks';
import type { Onboarding, OnboardingTemplate, SLA, Verification } from './linkTypes';

function OnboardingTab({ partner }: { partner: Partner }) {
  const key = [`/partners/${partner.id}/onboarding`];
  const o = useQuery({ queryKey: key, queryFn: () => api.get<Onboarding>(`/partners/${partner.id}/onboarding`) });
  const tpls = useQuery({ queryKey: ['/partners/onboarding/templates'], queryFn: () => api.list<OnboardingTemplate>('/partners/onboarding/templates') });
  const [tpl, setTpl] = useState<string>();
  const start = useAction((template: string) => api.post(`/partners/${partner.id}/onboarding`, { template }), { invalidate: [key] });
  const tick = useAction((v: { id: string; done: boolean }) => api.patch(`/partners/${partner.id}/onboarding/items/${v.id}`, { done: v.done }), { invalidate: [key], success: false });
  const test = useAction(() => api.post('/onboarding/tests', { partnerId: partner.id }), { invalidate: [key], success: false });
  if (!o.data) return null;
  if (!o.data.started) {
    return (
      <Card size="small">
        <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text>Start onboarding from a template.</Typography.Text>
          <Select style={{ width: '100%' }} placeholder="Template" value={tpl} onChange={setTpl}
            options={(tpls.data?.items ?? []).map((x) => ({ value: x.key, label: `${x.name} · ${x.description}` }))} />
          <Button type="primary" disabled={!tpl} loading={start.isPending} onClick={() => tpl && start.mutate(tpl)}>Start</Button>
        </Space>
      </Card>
    );
  }
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small">
        <Space size={24} align="center">
          <Progress type="circle" percent={o.data.score} size={80} />
          <div>
            <Typography.Title level={5} style={{ margin: 0 }}>Readiness</Typography.Title>
            <Typography.Text type="secondary">{o.data.template}</Typography.Text>
          </div>
          <Button icon={<ExperimentOutlined />} loading={test.isPending} onClick={() => test.mutate(undefined)}>Send test message</Button>
        </Space>
      </Card>
      <List
        size="small"
        bordered
        dataSource={o.data.items}
        renderItem={(it) => (
          <List.Item extra={it.kind === 'test' ? <Tag>test</Tag> : it.doneBy ? <Typography.Text type="secondary">{it.doneBy}</Typography.Text> : null}>
            <Checkbox checked={it.done} disabled={it.kind === 'test'} onChange={(e) => tick.mutate({ id: it.id, done: e.target.checked })}>
              {it.title}
            </Checkbox>
          </List.Item>
        )}
      />
      <Card size="small" title="Test runs">
        <List
          size="small"
          dataSource={o.data.tests}
          locale={{ emptyText: <Empty description="No test runs yet" /> }}
          renderItem={(r) => (
            <List.Item>
              <Space>
                <StatusTag status={r.passed ? 'passed' : 'failed'} />
                <Tag>{r.channel}</Tag>
                <Typography.Text type="secondary">{dayjs(r.createdAt).format('YYYY-MM-DD HH:mm')}</Typography.Text>
                {typeof r.detail?.error === 'string' && <Typography.Text type="danger">{r.detail.error}</Typography.Text>}
              </Space>
            </List.Item>
          )}
        />
      </Card>
    </Space>
  );
}

function SLATab({ partner }: { partner: Partner }) {
  const q = useQuery({ queryKey: ['/messages/slas', partner.id], queryFn: () => api.list<SLA>('/messages/slas', { partnerId: partner.id }) });
  const cur = q.data?.items[0];
  const [hours, setHours] = useState<number | null>(null);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const save = useAction(() => api.put(`/messages/slas/${partner.id}`, { ackHours: hours ?? cur?.ackHours ?? 24, enabled: enabled ?? cur?.enabled ?? true }), {
    invalidate: [['/messages/slas']],
  });
  return (
    <Card size="small">
      <Space direction="vertical">
        <Typography.Text>Outbound messages must be acknowledged by this partner within:</Typography.Text>
        <Space>
          <InputNumber min={1} max={720} value={hours ?? cur?.ackHours ?? 24} onChange={(v) => setHours(v)} addonAfter="hours" />
          <Switch checked={enabled ?? cur?.enabled ?? true} onChange={setEnabled} checkedChildren="alerts" unCheckedChildren="off" />
          <Button type="primary" loading={save.isPending} onClick={() => save.mutate(undefined)}>Save</Button>
        </Space>
      </Space>
    </Card>
  );
}

function CredentialsTab({ partner }: { partner: Partner }) {
  const key = ['/credentials/verifications', partner.id];
  const q = useQuery({ queryKey: key, queryFn: () => api.list<Verification>('/credentials/verifications', { partnerId: partner.id }) });
  const [text, setText] = useState('');
  let parsed: unknown;
  try {
    parsed = text ? JSON.parse(text) : undefined;
  } catch {
    parsed = undefined;
  }
  const verify = useAction(() => api.post<Verification>('/credentials/verify', { credential: parsed, partnerId: partner.id }), { invalidate: [key], success: false });
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card size="small" title="Verify a presented credential">
        <Input.TextArea rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder="Paste the credential JSON" style={{ fontFamily: 'monospace' }} />
        {text && !parsed && <Alert type="warning" showIcon title="Not valid JSON" style={{ marginTop: 8 }} />}
        <Button type="primary" icon={<SafetyCertificateOutlined />} disabled={!parsed} loading={verify.isPending} onClick={() => verify.mutate(undefined)} style={{ marginTop: 8 }}>
          Verify
        </Button>
      </Card>
      <List
        dataSource={q.data?.items ?? []}
        locale={{ emptyText: <Empty description="No credentials verified for this partner" /> }}
        renderItem={(v) => (
          <List.Item>
            <Descriptions size="small" column={1} title={<Space><StatusTag status={v.verified ? 'verified' : 'failed'} />{v.subject || v.credentialId}</Space>}>
              <Descriptions.Item label="Issuer"><Typography.Text code>{v.issuer}</Typography.Text></Descriptions.Item>
              <Descriptions.Item label="Checked">{dayjs(v.checkedAt).format('YYYY-MM-DD HH:mm')}</Descriptions.Item>
              <Descriptions.Item label="Checks">
                <Space wrap>
                  {v.checks.map((c) => (
                    <Tag key={c.name} color={c.ok ? 'success' : 'error'} title={c.detail}>{c.name}{c.ok ? '' : `: ${c.detail ?? ''}`}</Tag>
                  ))}
                </Space>
              </Descriptions.Item>
            </Descriptions>
          </List.Item>
        )}
      />
    </Space>
  );
}

/** Partner details: onboarding checklist and tests, SLA, and verified credentials. */
export function PartnerDrawer({ partner, onClose }: { partner: Partner | null; onClose: () => void }) {
  return (
    <Drawer open={!!partner} onClose={onClose} title={partner?.name} size="large" destroyOnHidden>
      {partner && (
        <Tabs
          items={[
            { key: 'onboarding', label: 'Onboarding', children: <OnboardingTab partner={partner} /> },
            { key: 'sla', label: 'SLA', children: <SLATab partner={partner} /> },
            { key: 'credentials', label: 'Credentials', children: <CredentialsTab partner={partner} /> },
          ]}
        />
      )}
    </Drawer>
  );
}
