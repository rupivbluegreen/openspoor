import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The Go API listens on :8080 in development; override with OPENSPOOR_API.
const api = process.env.OPENSPOOR_API ?? 'http://localhost:8080';

export default defineConfig({
  base: '/operator/',
  plugins: [react()],
  server: {
    port: 5174,
    proxy: { '/api': { target: api, changeOrigin: true } },
  },
  preview: { port: 5174 },
  build: {
    chunkSizeWarningLimit: 1600,
  },
});
