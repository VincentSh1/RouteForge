import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api/overview': {
        target: 'http://127.0.0.1:8081',
        rewrite: (path) => path.replace(/^\/api\/overview/, '/admin/v1/overview'),
      },
      '/api/requests': {
        target: 'http://127.0.0.1:8081',
        rewrite: (path) => path.replace(/^\/api\/requests/, '/admin/v1/requests'),
      },
    },
  },
  test: { environment: 'jsdom', setupFiles: ['./src/test-setup.ts'] },
});
