import {test as base, expect, type Page} from '@playwright/test';

export const adminEmail = process.env.RKA_E2E_ADMIN_EMAIL || 'admin@example.com';
export const adminPassword = process.env.RKA_E2E_ADMIN_PASSWORD || 'rka-e2e-admin-password';

export type Account = {email: string; password: string};

// signIn authenticates through the public API so the browser receives the
// HttpOnly session cookie exactly as a real user would.
export async function signIn(page: Page, account: Account = {email: adminEmail, password: adminPassword}) {
  const response = await page.request.post('/api/auth/login', {data: account});
  if (!response.ok()) {
    throw new Error(`login failed: ${response.status()} ${await response.text()}`);
  }
  return account;
}

// createLearner creates an isolated account for a test and signs the page in as
// it. Admin credentials are required for the account creation call.
export async function createLearner(page: Page, prefix = 'learner'): Promise<Account> {
  const email = `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2, 8)}@example.com`;
  const password = 'isolated-learner-password';
  const response = await page.request.post('/api/admin/users', {
    data: {email, password, role: 'learner', displayName: '测试学习者'},
  });
  if (!response.ok()) {
    throw new Error(`create learner failed: ${response.status()} ${await response.text()}`);
  }
  await page.request.post('/api/auth/logout');
  return signIn(page, {email, password});
}

export async function signOut(page: Page) {
  await page.request.post('/api/auth/logout');
}

export const test = base.extend<{signedIn: Account}>({
  signedIn: async ({page}, use) => {
    const account = await signIn(page);
    await use(account);
  },
});

export {expect};
