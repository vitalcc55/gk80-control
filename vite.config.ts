import { fileURLToPath, URL } from 'node:url';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { normalizePath } from 'vite';
import { defineConfig } from 'vitest/config';

const temporaryDir = normalizePath(fileURLToPath(new URL('./.tmp', import.meta.url)));

export default defineConfig(({ mode }) => ({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: { watch: { ignored: [temporaryDir, `${temporaryDir}/**`] } },
  build:
    mode === 'desktop' || mode === 'browser'
      ? { rollupOptions: { input: fileURLToPath(new URL(`./${mode}.html`, import.meta.url)) } }
      : {},
  test: {
    environment: 'jsdom',
    setupFiles: ['./tests/setup.ts'],
    exclude: ['tests/e2e/**', '**/node_modules/**', '.tmp/**'],
  },
}));
