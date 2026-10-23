import { useState } from 'react';
import { Alert, Button, Card, Checkbox, Collapse, Flex, Form, Input, List, Radio, Tag, Typography } from 'antd';
import { BellOutlined, CalendarOutlined, FileTextOutlined, FlagOutlined, LinkOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import { useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  api, ApiError, buildExpiryIcs, downloadBlob, errorMessage, icsFilename, parseIsoDate, REPORT_REASONS,
  type ConsumerInfo, type RecallAlertResult, type ReportReason, type VerifyReportInput,
} from '@openspoor/ui';

const isHttps = (u?: string): u is string => !!u && /^https:\/\//i.test(u);

/** Formats YYYY-MM-DD as a long date in the UI language. */
export function useLongDate() {
  const { i18n } = useTranslation();
  return (v: string) => {
    const d = parseIsoDate(v);
    return d ? d.toLocaleDateString(i18n.language, { dateStyle: 'long', timeZone: 'UTC' }) : v;
  };
}

/** Leaflet, allergens, origin and certificates published by the brand owner. */
export function ProductInfo({ info }: { info: ConsumerInfo }) {
  const { t } = useTranslation();
  const hasAny = isHttps(info.leafletUrl) || info.leafletText || info.allergens?.length || info.origin || info.certificates?.length;
  if (!hasAny) return null;
  return (
    <Card title={t('verify.aboutProduct')} lang={info.lang}>
      <Flex vertical gap={16}>
        {(isHttps(info.leafletUrl) || info.leafletText) && (
          <div>
            <Typography.Text strong>{t('verify.leaflet')}</Typography.Text>
            <Flex vertical gap={8} style={{ marginTop: 8 }}>
              {isHttps(info.leafletUrl) && (
                <Button href={info.leafletUrl} target="_blank" rel="noopener noreferrer" icon={<FileTextOutlined />} size="large" block>
                  {t('verify.openLeaflet')}
                </Button>
              )}
              {info.leafletText && (
                <Collapse
                  items={[{
                    key: 'text',
                    label: t('verify.readLeaflet'),
                    children: (
                      <Typography.Paragraph style={{ whiteSpace: 'pre-wrap', fontSize: 16, lineHeight: 1.6, marginBottom: 0, maxHeight: '60vh', overflowY: 'auto' }}>
                        {info.leafletText}
                      </Typography.Paragraph>
                    ),
                  }]}
                />
              )}
            </Flex>
          </div>
        )}
        {info.allergens?.length > 0 && (
          <div>
            <Typography.Text strong>{t('verify.allergens')}</Typography.Text>
            <Flex wrap gap={6} style={{ marginTop: 8 }}>
              {info.allergens.map((a) => (
                <Tag key={a} color="orange" style={{ fontSize: 14, padding: '2px 10px', marginInlineEnd: 0 }}>{a}</Tag>
              ))}
            </Flex>
          </div>
        )}
        {info.origin && (
          <div>
            <Typography.Text strong>{t('verify.origin')}</Typography.Text>
            <Typography.Paragraph style={{ marginBottom: 0, marginTop: 4 }}>{info.origin}</Typography.Paragraph>
          </div>
        )}
        {info.certificates?.length > 0 && (
          <div>
            <Typography.Text strong>{t('verify.certificates')}</Typography.Text>
            <List
              size="small"
              dataSource={info.certificates}
              renderItem={(c) => (
                <List.Item style={{ paddingInline: 0 }}>
                  <List.Item.Meta
                    avatar={<SafetyCertificateOutlined style={{ fontSize: 18, color: '#2F8F46' }} />}
                    title={isHttps(c.url) ? <a href={c.url} target="_blank" rel="noopener noreferrer">{c.name} <LinkOutlined /></a> : c.name}
                    description={c.issuer ? t('verify.issuedBy', { issuer: c.issuer }) : undefined}
                  />
                </List.Item>
              )}
            />
          </div>
        )}
      </Flex>
    </Card>
  );
}

/** Builds a calendar file in the browser; nothing is sent anywhere. */
export function ExpiryReminder({ expiryDate, product, lot }: { expiryDate: string; product: string; lot?: string }) {
  const { t } = useTranslation();
  const longDate = useLongDate();
  const d = parseIsoDate(expiryDate);
  const now = new Date();
  if (!d || d.getTime() < Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())) return null;
  const date = longDate(expiryDate);
  const download = () => {
    const ics = buildExpiryIcs({
      expiryDate,
      summary: t('verify.expirySummary', { product }),
      description: lot ? t('verify.expiryDescription', { lot, date }) : t('verify.expiresOn', { date }),
    });
    if (ics) downloadBlob(ics, icsFilename(product), 'text/calendar;charset=utf-8');
  };
  return (
    <Card title={<><CalendarOutlined /> {t('verify.expiryTitle')}</>}>
      <Flex vertical gap={12}>
        <Typography.Text style={{ fontSize: 16 }}>{t('verify.expiresOn', { date })}</Typography.Text>
        <Button size="large" block icon={<CalendarOutlined />} onClick={download}>{t('verify.addReminder')}</Button>
        <Typography.Text type="secondary">{t('verify.reminderHint')}</Typography.Text>
      </Flex>
    </Card>
  );
}

/** Opt-in, single-use recall notice for this pack's batch. */
export function RecallAlerts({ code, lot }: { code: string; lot: string }) {
  const { t, i18n } = useTranslation();
  const longDate = useLongDate();
  const [unavailable, setUnavailable] = useState(false);
  const sub = useMutation({
    mutationFn: (v: { email: string }) =>
      api.post<RecallAlertResult>('/verify/recall-alerts', { code, email: v.email.trim(), consent: true, lang: i18n.language.slice(0, 2) }, { anonymous: true }),
    onError: (e) => {
      if (e instanceof ApiError && e.status === 409) setUnavailable(true);
    },
    meta: { silent: true },
  });
  if (unavailable) return null;
  return (
    <Card title={<><BellOutlined /> {t('verify.recallTitle')}</>}>
      {sub.data ? (
        <Alert type="success" showIcon title={t('verify.recallSubscribed', { lot, until: longDate(sub.data.until) })} />
      ) : (
        <Form layout="vertical" requiredMark={false} onFinish={(v: { email: string }) => sub.mutate(v)} disabled={sub.isPending}>
          <Form.Item name="email" label={t('verify.emailLabel')} rules={[{ required: true, type: 'email', whitespace: true, message: t('verify.emailInvalid') }]}>
            <Input type="email" inputMode="email" autoComplete="email" size="large" dir="ltr" maxLength={254} />
          </Form.Item>
          <Form.Item
            name="consent"
            valuePropName="checked"
            rules={[{ validator: (_r, v) => (v ? Promise.resolve() : Promise.reject(new Error(t('verify.consentRequired')))) }]}
          >
            <Checkbox>{t('verify.recallConsent', { lot })}</Checkbox>
          </Form.Item>
          {sub.error && !(sub.error instanceof ApiError && sub.error.status === 409) && (
            <Alert style={{ marginBottom: 12 }} type="error" showIcon title={errorMessage(sub.error).title} description={errorMessage(sub.error).description} />
          )}
          <Button type="primary" htmlType="submit" size="large" block loading={sub.isPending} icon={<BellOutlined />}>
            {t('verify.notifyMe')}
          </Button>
        </Form>
      )}
    </Card>
  );
}

interface ReportValues {
  reason: ReportReason;
  details?: string;
  country?: string;
}

/** Anonymous "report a suspect product": no personal data fields. */
export function ReportSuspect({ code }: { code: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const report = useMutation({
    mutationFn: (v: ReportValues) => {
      const body: VerifyReportInput = { code, reason: v.reason };
      if (v.details?.trim()) body.details = v.details.trim();
      if (v.country?.trim()) body.country = v.country.trim().toUpperCase();
      return api.post<{ received: boolean }>('/verify/report', body, { anonymous: true });
    },
    meta: { silent: true },
  });
  if (report.isSuccess) return <Alert type="success" showIcon title={t('verify.reportThanks')} />;
  if (!open)
    return (
      <Button size="large" block danger icon={<FlagOutlined />} onClick={() => setOpen(true)}>
        {t('verify.reportTitle')}
      </Button>
    );
  return (
    <Card title={<><FlagOutlined /> {t('verify.reportTitle')}</>}>
      <Typography.Paragraph type="secondary">{t('verify.reportIntro')}</Typography.Paragraph>
      <Form<ReportValues> layout="vertical" requiredMark={false} onFinish={(v) => report.mutate(v)} disabled={report.isPending}>
        <Form.Item name="reason" label={t('verify.reportReason')} rules={[{ required: true, message: t('common.required') }]}>
          <Radio.Group style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {REPORT_REASONS.map((r) => (
              <Radio key={r} value={r} style={{ fontSize: 15 }}>{t(`consumerReason.${r}`)}</Radio>
            ))}
          </Radio.Group>
        </Form.Item>
        <Form.Item name="details" label={t('verify.reportDetails')}>
          <Input.TextArea maxLength={1000} showCount autoSize={{ minRows: 3, maxRows: 8 }} placeholder={t('verify.reportDetailsPlaceholder')} />
        </Form.Item>
        <Form.Item
          name="country"
          label={t('verify.reportCountry')}
          normalize={(v?: string) => v?.toUpperCase()}
          rules={[{ pattern: /^[A-Za-z]{2}$/, message: t('verify.reportCountryInvalid') }]}
        >
          <Input maxLength={2} placeholder="NL" style={{ width: 96 }} dir="ltr" autoComplete="country" />
        </Form.Item>
        {report.error && <Alert style={{ marginBottom: 12 }} type="error" showIcon title={errorMessage(report.error).title} description={errorMessage(report.error).description} />}
        <Flex gap={8}>
          <Button onClick={() => setOpen(false)} size="large">{t('action.cancel')}</Button>
          <Button type="primary" danger htmlType="submit" size="large" loading={report.isPending} style={{ flex: 1 }}>{t('verify.reportSend')}</Button>
        </Flex>
      </Form>
    </Card>
  );
}
