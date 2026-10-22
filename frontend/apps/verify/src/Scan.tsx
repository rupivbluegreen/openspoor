import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Descriptions, Flex, Input, Result, Typography } from 'antd';
import { CameraOutlined, CheckCircleFilled, CloseCircleFilled, ExclamationCircleFilled, QuestionCircleFilled, StopOutlined, WarningFilled } from '@ant-design/icons';
import { useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type VerifyScanResult } from '@openspoor/ui';
import { cameraAvailable, createDetector } from './barcode';

type Kind = 'verified' | 'suspect' | 'notFound' | 'recalled' | 'expired' | 'unknown';

export function classify(r: VerifyScanResult): Kind {
  const s = String(r.status ?? '').toLowerCase().replace(/[\s-]/g, '_');
  if (s.includes('recall')) return 'recalled';
  if (s.includes('expir')) return 'expired';
  if (s.includes('suspect') || s.includes('decommission') || s.includes('illegitimate') || s.includes('counterfeit')) return 'suspect';
  if (s === 'not_found' || s === 'notfound' || s === 'unknown_product' || s === 'unknown_serial') return 'notFound';
  if (s === 'verified' || s === 'ok' || s === 'valid' || (r.verified && !s)) return 'verified';
  return r.verified ? 'verified' : 'unknown';
}

const LOOK: Record<Kind, { status: 'success' | 'warning' | 'error' | 'info'; icon: React.ReactNode; color: string }> = {
  verified: { status: 'success', icon: <CheckCircleFilled />, color: '#2F8F46' },
  suspect: { status: 'warning', icon: <WarningFilled />, color: '#C98A0B' },
  notFound: { status: 'error', icon: <QuestionCircleFilled />, color: '#C0392B' },
  recalled: { status: 'error', icon: <CloseCircleFilled />, color: '#C0392B' },
  expired: { status: 'error', icon: <ExclamationCircleFilled />, color: '#C0392B' },
  unknown: { status: 'info', icon: <QuestionCircleFilled />, color: '#1F6F8B' },
};

function Camera({ onCode, onStop }: { onCode: (c: string) => void; onStop: () => void }) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let stream: MediaStream | null = null;
    let timer: number | undefined;
    let stopped = false;
    (async () => {
      const detector = await createDetector();
      if (!detector) {
        setErr(t('verify.cameraUnsupported'));
        return;
      }
      try {
        stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: { ideal: 'environment' } }, audio: false });
        if (stopped || !video.current) return;
        video.current.srcObject = stream;
        await video.current.play();
        const tick = async () => {
          if (stopped || !video.current) return;
          try {
            const found = await detector.detect(video.current);
            if (found[0]?.rawValue) {
              onCode(found[0].rawValue);
              return;
            }
          } catch {
            /* frame not ready */
          }
          timer = window.setTimeout(tick, 250);
        };
        void tick();
      } catch (e) {
        setErr(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      stream?.getTracks().forEach((tr) => tr.stop());
    };
  }, [onCode, t]);

  return (
    <Flex vertical gap={8}>
      {err ? (
        <Alert type="warning" showIcon title={err} />
      ) : (
        <video ref={video} muted playsInline style={{ width: '100%', borderRadius: 8, background: '#000', aspectRatio: '1 / 1', objectFit: 'cover' }} />
      )}
      <Button icon={<StopOutlined />} onClick={onStop} block>
        {t('action.stopScan')}
      </Button>
    </Flex>
  );
}

export default function Scan() {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [scanning, setScanning] = useState(false);
  const verify = useMutation({ mutationFn: async (c: string) => {
      try {
        return await api.post<VerifyScanResult>('/verify/scan', { code: c }, { anonymous: true });
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return { verified: false, status: 'not_found', message: e.title } satisfies VerifyScanResult;
        throw e;
      }
    }, meta: { silent: true } });
  const submit = (c: string) => {
    const v = c.trim();
    if (!v) return;
    setScanning(false);
    setCode(v);
    verify.mutate(v);
  };

  const r = verify.data;
  const kind = r ? classify(r) : null;
  const look = kind ? LOOK[kind] : null;
  const pack = { ...(r?.product ?? {}), ...(r?.pack ?? {}) } as Record<string, unknown>;

  return (
    <Flex vertical gap={16} style={{ maxWidth: 560, margin: '0 auto' }}>
      <div>
        <Typography.Title level={3} style={{ marginBottom: 0 }}>{t('verify.title')}</Typography.Title>
        <Typography.Text type="secondary">{t('verify.subtitle')}</Typography.Text>
      </div>

      {r && look && kind ? (
        <Card styles={{ body: { padding: 8 } }} style={{ borderColor: look.color, borderWidth: 2 }}>
          <Result
            status={look.status}
            icon={<span style={{ color: look.color, fontSize: 88 }}>{look.icon}</span>}
            title={<span style={{ color: look.color, fontSize: 32, fontWeight: 700 }}>{t(`verify.${kind}`)}</span>}
            subTitle={r.message}
            extra={<Button type="primary" size="large" icon={<CameraOutlined />} onClick={() => { verify.reset(); setCode(''); }}>{t('verify.scanAgain')}</Button>}
          />
          <Descriptions
            column={1}
            size="small"
            bordered
            styles={{ label: { width: 110 } }}
            items={[
              { key: 'name', label: t('field.product'), children: String(r.product?.name ?? '–') },
              { key: 'gtin', label: 'GTIN', children: <span dir="ltr">{String(pack.gtin ?? '–')}</span> },
              { key: 'serial', label: t('field.serial'), children: <span dir="ltr">{String(pack.serial ?? '–')}</span> },
              { key: 'lot', label: t('field.lot'), children: String(pack.lot ?? '–') },
              { key: 'expiry', label: t('field.expiry'), children: String(pack.expiry ?? '–') },
            ]}
          />
        </Card>
      ) : (
        <>
          <Card>
            {scanning ? (
              <Camera onCode={submit} onStop={() => setScanning(false)} />
            ) : (
              <Button type="primary" size="large" block icon={<CameraOutlined />} disabled={!cameraAvailable()} onClick={() => setScanning(true)} style={{ height: 64, fontSize: 18 }}>
                {t('verify.startCamera')}
              </Button>
            )}
            {!cameraAvailable() && <Alert style={{ marginTop: 12 }} type="info" showIcon title={t('verify.cameraUnsupported')} />}
          </Card>
          <Card title={t('verify.manual')}>
            <Flex vertical gap={12}>
              <Input.TextArea
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder={t('verify.manualPlaceholder')}
                autoSize={{ minRows: 3, maxRows: 6 }}
                dir="ltr"
                style={{ fontFamily: 'monospace' }}
              />
              <Button type="primary" size="large" block loading={verify.isPending} disabled={!code.trim()} onClick={() => submit(code)}>
                {t('action.verify')}
              </Button>
            </Flex>
          </Card>
          {verify.error && <Alert type="error" showIcon title={(verify.error as Error).message} />}
        </>
      )}
    </Flex>
  );
}
