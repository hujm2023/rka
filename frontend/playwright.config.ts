import {defineConfig} from '@playwright/test';

// End-to-end tests run against the production shape: the Go application serves
// the built browser bundle, the API and the database. No Vite dev server is
// involved, so the tests prove the delivered artifact.
export default defineConfig({
  testDir: './tests',
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: process.env.RKA_E2E_BASE_URL || 'http://127.0.0.1:4199',
    headless: true,
    viewport: {width: 1440, height: 1000},
    launchOptions: {args: ['--no-sandbox']},
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'bash ../scripts/e2e-server.sh',
    url: 'http://127.0.0.1:4199/healthz',
    reuseExistingServer: false,
    timeout: 300_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
  reporter: 'list',
});
