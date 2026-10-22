import { useCallback, useState, type ReactNode } from 'react';
import { Alert } from 'antd';
import { SafetyCertificateOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { ModalForm, ProFormSelect, ProFormText, ProFormTextArea } from '../pro';
import { api } from '../api/client';
import type { ESignature } from '../api/types';
import { notifyError, notifySuccess } from '../notify';

export const SIGNATURE_MEANINGS = ['approval', 'review', 'authorship', 'responsibility'] as const;

export interface ESignatureModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  entity: string;
  entityId: string;
  defaultMeaning?: (typeof SIGNATURE_MEANINGS)[number];
  defaultReason?: string;
  title?: ReactNode;
  onSigned?: (sig: ESignature) => void;
}

/** 21 CFR Part 11-style signing dialog: password re-auth + meaning + reason → POST /esignatures. */
export function ESignatureModal(props: ESignatureModalProps) {
  const { t } = useTranslation();
  const labels: Record<string, string> = {
    approval: t('esig.meaningApprove'),
    review: t('esig.meaningReview'),
    authorship: t('esig.meaningAuthor'),
    responsibility: t('esig.meaningResponsibility'),
  };
  return (
    <ModalForm<{ password: string; meaning: string; reason: string }>
      title={
        <span>
          <SafetyCertificateOutlined /> {props.title ?? t('esig.title')}
        </span>
      }
      open={props.open}
      onOpenChange={props.onOpenChange}
      width={480}
      modalProps={{ destroyOnHidden: true }}
      initialValues={{ meaning: props.defaultMeaning ?? 'approval', reason: props.defaultReason }}
      submitter={{ searchConfig: { submitText: t('action.sign'), resetText: t('action.cancel') } }}
      onFinish={async (v) => {
        try {
          const sig = await api.post<ESignature>('/esignatures', { ...v, entity: props.entity, entityId: props.entityId });
          notifySuccess(t('esig.signed'));
          props.onSigned?.(sig);
          return true;
        } catch (e) {
          notifyError(e);
          return false;
        }
      }}
    >
      <Alert type="info" showIcon style={{ marginBottom: 16 }} title={t('esig.intro')} description={`${props.entity} · ${props.entityId}`} />
      <ProFormSelect
        name="meaning"
        label={t('field.meaning')}
        rules={[{ required: true }]}
        options={SIGNATURE_MEANINGS.map((m) => ({ value: m, label: labels[m] }))}
      />
      <ProFormTextArea name="reason" label={t('field.reason')} rules={[{ required: true }]} fieldProps={{ rows: 2 }} />
      <ProFormText.Password name="password" label={t('field.password')} rules={[{ required: true }]} fieldProps={{ autoComplete: 'current-password' }} />
    </ModalForm>
  );
}

interface Pending {
  entity: string;
  entityId: string;
  defaultMeaning?: ESignatureModalProps['defaultMeaning'];
  defaultReason?: string;
  resolve: (s: ESignature | null) => void;
}

/** Imperative helper: `const sig = await requestSignature({entity, entityId})` (null when cancelled). */
export function useESignature() {
  const [pending, setPending] = useState<Pending | null>(null);
  const requestSignature = useCallback(
    (o: Omit<Pending, 'resolve'>) => new Promise<ESignature | null>((resolve) => setPending({ ...o, resolve })),
    [],
  );
  const holder = pending ? (
    <ESignatureModal
      open
      entity={pending.entity}
      entityId={pending.entityId}
      defaultMeaning={pending.defaultMeaning}
      defaultReason={pending.defaultReason}
      onOpenChange={(o) => {
        if (!o) {
          pending.resolve(null);
          setPending(null);
        }
      }}
      onSigned={(s) => {
        pending.resolve(s);
        setPending(null);
      }}
    />
  ) : null;
  return { requestSignature, holder };
}
