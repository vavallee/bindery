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
    // A backstop for a loaded CI runner, not a licence for slow tests. The
    // 5 s default failed requests.test.tsx on an unchanged tree (#3606); fix
    // the test first, the timeout only absorbs scheduler noise.
    testTimeout: 15_000,
    setupFiles: ['./src/test-localstorage.ts', './src/test-setup.ts'],
    // Only collected with `npm run test:coverage` (CI uploads web/coverage/
    // lcov.info to Codecov under the `frontend` flag). Plain `npm test` stays
    // as fast as before.
    coverage: {
      provider: 'v8',
      // projectRoot makes lcov paths repo relative (web/src/...), so Codecov
      // matches them to files without any path fixing.
      reporter: ['text-summary', ['lcov', { projectRoot: '..' }]],
      reportsDirectory: './coverage',
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/test/**',
        'src/test-*.{ts,tsx}',
        'src/**/*.d.ts',
      ],
    },
  },
})
