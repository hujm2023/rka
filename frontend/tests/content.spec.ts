// Knowledge library and reader: full original text, version-backed reading
// progress and deep-link compatibility with the previous lesson index.
import {expect, test} from '@playwright/test';
import fs from 'node:fs/promises';
import {catalogOf, findMaterial, getJson, signInIsolatedLearner} from './support/e2e';

const normalizeTitle = (value: string) => value.replace(/[*`📚📕📘📗📙⭐🔥🚀]/gu, '').trim();

async function summaryOf(page: any): Promise<any> {
  return getJson(page, '/api/learning/summary');
}

test('material search, original-text fidelity, chapter navigation and reading progress', async ({page}) => {
  await signInIsolatedLearner(page, 'content-reader');
  const catalog = await catalogOf(page);
  const textbooks = catalog.materials.filter((material: any) => material.kind === 'textbook');
  expect(textbooks.length).toBeGreaterThanOrEqual(20);
  const material = findMaterial(catalog, '第6章');

  await page.goto('/knowledge');
  await expect(page.locator('.material-card')).toHaveCount(textbooks.length);
  await page.getByRole('textbox', {name: '搜索知识全文'}).fill('NoSQL');
  const card = page.locator('.material-card').filter({has: page.getByRole('heading', {name: '数据库设计基础知识', exact: true})});
  await expect(card).toBeVisible();
  await card.click();

  // The reader serves the complete original, not an excerpt or a summary.
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  const rawResponse = await page.request.get(`/api/content/materials/${material.id}/raw`);
  expect(rawResponse.status()).toBe(200);
  const raw = await rawResponse.text();
  expect(raw).toBe(await fs.readFile(`../content/materials/${material.id}.md`, 'utf8'));
  expect(raw.length).toBe(material.chars);
  const content = await getJson<any>(page, `/api/content/materials/${material.id}/content`);
  expect(content.content).toBe(raw);
  expect(content.materialId).toBe(material.id);
  expect(content.materialVersionId).toBeTruthy();

  // Chapter navigation positions the selected section under the reader toolbar.
  const section = material.sections.filter((entry: any) => entry.level === 2).at(-1);
  await page.locator('.toc-scroll').getByRole('button', {name: section.title, exact: true}).click();
  await expect.poll(async () => page.locator(`[id="${section.id}"]`).evaluate((element) => Math.round(element.getBoundingClientRect().top))).toBeLessThan(160);

  // Reading progress is reported to the server as evidence bound to the version.
  await expect.poll(async () => {
    const summary = await summaryOf(page);
    return summary.counts?.reading ?? 0;
  }, {timeout: 15_000}).toBeGreaterThan(0);
  const summary = await summaryOf(page);
  const progress = (summary.readingProgress || []).find((entry: any) => entry.materialId === material.id);
  expect(progress).toBeTruthy();
  expect(progress.materialVersionId).toBeTruthy();
  expect(progress.progress).toBeGreaterThan(0);
  expect(progress.sectionAnchor).toBeTruthy();

  await page.reload();
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  await expect.poll(async () => page.evaluate(() => window.scrollY)).toBeGreaterThan(500);

  await page.getByRole('button', {name: '标记为已学', exact: true}).click();
  await expect(page.getByRole('button', {name: '已标记学完', exact: true})).toBeVisible();
  await expect.poll(async () => {
    const after = await summaryOf(page);
    const entry = (after.readingProgress || []).find((row: any) => row.materialId === material.id);
    return entry?.done === true;
  }, {timeout: 15_000}).toBe(true);
  await page.reload();
  await expect(page.getByRole('button', {name: '已标记学完', exact: true})).toBeVisible();
});

test('material and legacy lesson deep links still open the original at the right section', async ({page}) => {
  await signInIsolatedLearner(page, 'content-deeplink');
  const catalog = await catalogOf(page);
  const material = findMaterial(catalog, '第6章');
  const section = material.sections.find((entry: any) => entry.level === 2 && entry.title.includes('6.3'));
  expect(section).toBeTruthy();

  // New deep-link shape: materialId + sectionId inside the current version.
  await page.goto(`/knowledge?id=${material.id}&section=${section.id}`);
  await expect(page.locator('.page-heading h1').first()).toHaveText(material.title);
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  await expect.poll(async () => page.locator(`[id="${section.id}"]`).evaluate((element) => Math.round(element.getBoundingClientRect().top))).toBeLessThan(160);

  // Links coming back from a practice question keep the return affordance.
  await page.goto(`/knowledge?id=${material.id}&section=${section.id}&from=practice`);
  await expect(page.locator('.reading-return-banner')).toContainText('正在回看本题关联原文');
  const back = page.getByRole('link', {name: '返回本题继续练习', exact: true});
  await expect(back).toBeVisible();
  expect(await back.getAttribute('href')).toContain('/practice');
  await back.click();
  await expect(page).toHaveURL(/\/practice$/);

  // Compatibility: the old lesson index still resolves to the same material
  // section (the knowledge page falls back from lessonId to material+section).
  const lesson = (catalog.lessons || []).find((entry: any) => entry.sourceFile === material.sourceFile && entry.title.includes('6.2'));
  expect(lesson, 'legacy lesson for 6.2').toBeTruthy();
  const lessonResponse = await page.request.get(`/api/content/lessons/${encodeURIComponent(lesson.id)}`);
  expect(lessonResponse.status()).toBe(200);
  const lessonBody = await lessonResponse.json();
  expect(lessonBody.lesson.body).toBe(await fs.readFile(`../content/lessons/${lesson.id}.md`, 'utf8'));
  expect(lessonBody.lesson.materialId).toBe(material.id);
  expect(lessonBody.lesson.sectionKey).toBeTruthy();

  await page.goto(`/knowledge?id=${lesson.id}`);
  await expect(page.locator('.page-heading h1').first()).toHaveText(material.title);
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  const legacySection = material.sections.find((entry: any) => normalizeTitle(entry.title) === normalizeTitle(lesson.title));
  expect(legacySection, 'legacy lesson title maps to a current section').toBeTruthy();
  await expect.poll(async () => page.locator(`[id="${legacySection.id}"]`).evaluate((element) => Math.round(element.getBoundingClientRect().top))).toBeLessThan(200);
});

test('mobile chapter navigation opens the table of contents and locates a section', async ({page}) => {
  await signInIsolatedLearner(page, 'content-mobile');
  const catalog = await catalogOf(page);
  const material = findMaterial(catalog, '第6章');
  const section = material.sections.filter((entry: any) => entry.level === 2).at(-1);

  await page.setViewportSize({width: 390, height: 844});
  await page.goto(`/knowledge?id=${material.id}`);
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  const toggle = page.getByRole('button', {name: '章内目录', exact: true});
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await page.locator('.toc-scroll').getByRole('button', {name: section.title, exact: true}).click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await expect.poll(async () => page.locator(`[id="${section.id}"]`).evaluate((element) => Math.round(element.getBoundingClientRect().top))).toBeLessThan(180);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
