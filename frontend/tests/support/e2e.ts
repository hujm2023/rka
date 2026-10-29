// Shared helpers for the browser end-to-end suite.
//
// The platform owns identity on the server (HttpOnly cookie), so every test
// starts by signing in through the public API, exactly as the login page does.
// Isolated accounts are created for the tests that mutate learning data so the
// cases cannot influence each other.
import {expect, type APIRequestContext, type Browser, type BrowserContext, type Page} from '@playwright/test';
import {adminEmail, adminPassword, createLearner, signIn, type Account} from './auth';

export const baseURL = process.env.RKA_E2E_BASE_URL || 'http://127.0.0.1:4199';

export const options = ['A', 'B', 'C', 'D'] as const;
export type Option = typeof options[number];

// signInIsolatedLearner signs in as the administrator (required to create the
// account), creates a fresh learner and signs the page in as that learner.
export async function signInIsolatedLearner(page: Page, prefix = 'learner'): Promise<Account> {
  await signIn(page);
  return createLearner(page, prefix);
}

export async function getJson<T = any>(page: Page, path: string): Promise<T> {
  const response = await page.request.get(path);
  expect(response.ok(), `GET ${path} -> ${response.status()}`).toBeTruthy();
  return response.json() as Promise<T>;
}

export async function activeSession(page: Page): Promise<any | null> {
  const body = await getJson<{session?: unknown}>(page, '/api/practice/sessions/active');
  return (body?.session as any) ?? null;
}

export async function signInApi(request: APIRequestContext, account: Account): Promise<APIRequestContext> {
  const response = await request.post('/api/auth/login', {data: account});
  expect(response.ok(), `login ${account.email} -> ${response.status()}`).toBeTruthy();
  return request;
}

// adminApi is used when a test needs an administrator-only read (for example
// the reference solution) while the browser page stays a learner.
export async function adminApi(request: APIRequestContext): Promise<APIRequestContext> {
  return signInApi(request, {email: adminEmail, password: adminPassword});
}

export async function solutionFor(request: APIRequestContext, questionId: string): Promise<{answer?: string; analysis?: string} | null> {
  const response = await request.get(`/api/content/questions/${encodeURIComponent(questionId)}/solution`);
  if (!response.ok()) return null;
  return response.json();
}

// openDevice returns a second, completely separate browser context: its own
// cookie jar, IndexedDB and network emulation. It models a second device.
export async function openDevice(browser: Browser): Promise<{context: BrowserContext; page: Page}> {
  const context = await browser.newContext({baseURL, viewport: {width: 1440, height: 1000}});
  const page = await context.newPage();
  return {context, page};
}

export function optionIndex(option: Option): number {
  return options.indexOf(option);
}

// chooseOption picks an answer in the practice page without submitting it.
export async function chooseOption(page: Page, option: Option): Promise<void> {
  await page.getByRole('radio').nth(optionIndex(option)).click();
  await expect(page.getByRole('radio').nth(optionIndex(option))).toHaveAttribute('aria-checked', 'true');
}

export async function submitAnswer(page: Page): Promise<void> {
  await page.getByRole('button', {name: '提交答案', exact: true}).click();
}

// startPractice creates a free practice session (all questions) from the setup
// screen.
export async function startPractice(page: Page): Promise<void> {
  await page.getByRole('button', {name: '开始练习', exact: true}).click();
  await expect(page.getByRole('radio').first()).toBeVisible();
}

// markOf returns the server-side mark for a question, or null.
export async function markOf(page: Page, questionId: string): Promise<any | null> {
  const body = await getJson<{items?: any[]; marks?: any[]}>(page, '/api/practice/marks');
  const rows = body.items || body.marks || [];
  return rows.find((row) => row?.questionId === questionId || row?.id === questionId) ?? null;
}

// catalogOf fetches the server content index used by the assertions.
export async function catalogOf(page: Page): Promise<any> {
  return getJson(page, '/api/content/catalog');
}

export function findMaterial(catalog: any, titlePart: string, kind = 'textbook'): any {
  const material = (catalog.materials || []).find((entry: any) => entry.kind === kind && entry.title.includes(titlePart));
  expect(material, `material containing ${titlePart}`).toBeTruthy();
  return material;
}

// Re-export the provided account helpers so specs only import one support module.
export {adminEmail, adminPassword, createLearner, signIn, signOut} from './auth';
export type {Account} from './auth';
