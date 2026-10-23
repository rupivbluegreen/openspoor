import { useEffect, useRef } from 'react';
import { Button, Card, Flex, Result, Spin, Typography } from 'antd';
import { useMutation } from '@tanstack/react-query';
import { Link, useSearchParams } from 'react-router';
import { useTranslation } from 'react-i18next';
import { api } from '@openspoor/ui';

/** Target of the link in every recall notice: deletes the subscription for the token. */
export default function Unsubscribe() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const token = params.get('token')?.trim() ?? '';
  const sent = useRef(false);
  const unsub = useMutation({
    mutationFn: (tk: string) => api.post<{ unsubscribed: boolean }>('/verify/recall-alerts/unsubscribe', { token: tk }, { anonymous: true }),
    meta: { silent: true },
  });
  useEffect(() => {
    if (token && !sent.current) {
      sent.current = true;
      unsub.mutate(token);
    }
  }, [token, unsub]);

  const back = (
    <Link to="/">
      <Button type="primary" size="large">{t('verify.backToScan')}</Button>
    </Link>
  );
  return (
    <Flex vertical gap={16} style={{ maxWidth: 560, margin: '0 auto' }}>
      <Typography.Title level={3} style={{ marginBottom: 0 }}>{t('verify.unsubscribeTitle')}</Typography.Title>
      <Card>
        {!token ? (
          <Result status="warning" title={t('verify.unsubscribeMissing')} extra={back} />
        ) : unsub.isSuccess ? (
          <Result status="success" title={t('verify.unsubscribed')} extra={back} />
        ) : unsub.isError ? (
          <Result status="error" title={t('verify.unsubscribeFailed')} extra={<Button size="large" onClick={() => unsub.mutate(token)}>{t('action.submit')}</Button>} />
        ) : (
          <Flex vertical align="center" gap={12} style={{ padding: 32 }}>
            <Spin size="large" />
            <Typography.Text>{t('verify.unsubscribing')}</Typography.Text>
          </Flex>
        )}
      </Card>
    </Flex>
  );
}
