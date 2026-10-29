import { resolve } from 'node:path';
import { defineConfig } from '@playwright/test';

process.env['PLAYWRIGHT_BROWSERS_PATH'] ??= resolve('.cache/playwright');

export default defineConfig({
  testDir: './tests/e2e',
  use: { baseURL: 'http://127.0.0.1:5174', browserName: 'chromium' },
  webServer: {
    command: 'npm exec vite -- --host 127.0.0.1 --port 5174 --strictPort',
    url: 'http://127.0.0.1:5174',
    reuseExistingServer: false,
    timeout: 30000,
  },
});
