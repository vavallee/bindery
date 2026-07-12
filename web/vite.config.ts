import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [
    react(),
  ],
  server: {
    proxy: {
      '/api': 'http://localhost:8787',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Relative asset URLs (./assets/…) let the backend inject a <base> tag at
    // serve time, making prefix-mounted deploys work without a per-path rebuild.
    assetsDir: 'assets',
  },
  base: './',
  test: {
    environment: 'jsdom',
    environmentOptions: {
      jsdom: {
        url: 'http://localhost/',
      },
    },
    globals: true,
    setupFiles: ['./src/test-localstorage.ts', './src/test-setup.ts'],
  },
})
