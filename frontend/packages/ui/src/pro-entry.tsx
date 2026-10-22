/**
 * `@openspoor/ui/pro`: everything from the core entry plus ProComponents-based building blocks.
 * Apps that use ProLayout/ProTable/ProForm import from here; the public verify app does not.
 */
import type { ReactNode } from 'react';
import type { QueryClient } from '@tanstack/react-query';
import { arEGIntl, enUSIntl, nlNLIntl } from '@ant-design/pro-components';
import { AppProvider } from './AppProvider';
import { ProConfigProvider } from './pro';

export * from './index';
export * from './pro';
export { QueryTable, type QueryTableProps, type FetchParams } from './components/QueryTable';
export { ESignatureModal, useESignature, SIGNATURE_MEANINGS } from './components/ESignature';
export { JsonSchemaFields } from './components/JsonSchemaFields';
export { LoginCard } from './components/LoginCard';

const proIntl = { en: enUSIntl, nl: nlNLIntl, ar: arEGIntl };

/** AppProvider + ProConfigProvider (dark mode and locale for ProComponents). */
export function ProAppProvider({ children, queryClient }: { children: ReactNode; queryClient?: QueryClient }) {
  return (
    <AppProvider
      queryClient={queryClient}
      wrapper={(c, s) => (
        <ProConfigProvider dark={s.mode === 'dark'} intl={proIntl[s.lang]}>
          {c}
        </ProConfigProvider>
      )}
    >
      {children}
    </AppProvider>
  );
}
