import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ProAppProvider as AppProvider } from '@openspoor/ui/pro';
import { App } from './App';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AppProvider>
      <App />
    </AppProvider>
  </StrictMode>,
);
