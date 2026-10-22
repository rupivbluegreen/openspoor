import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ProAppProvider as AppProvider, tokenStore } from '@openspoor/ui/pro';
import { App } from './App';

// Operator sessions never share storage with tenant sessions.
tokenStore.key = 'openspoor.operator.token';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AppProvider>
      <App />
    </AppProvider>
  </StrictMode>,
);
