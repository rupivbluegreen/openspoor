import { Button, Result } from 'antd';
import { useNavigate } from 'react-router';
import { useTranslation } from 'react-i18next';

export default function NotFound() {
  const { t } = useTranslation();
  const nav = useNavigate();
  return <Result status="404" title="404" subTitle={t('common.notFound')} extra={<Button onClick={() => nav('/')}>{t('action.back')}</Button>} />;
}
