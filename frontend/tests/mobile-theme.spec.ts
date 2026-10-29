// Mobile layout, dark theme and themed diagrams. These cases guard the visual
// behaviour that the previous single-page frontend guaranteed.
import {expect, test, type Page} from '@playwright/test';
import {adminApi, catalogOf, findMaterial, signIn, signInIsolatedLearner, solutionFor} from './support/e2e';

async function contrast(page: Page, selector: string) {
  return page.locator(selector).first().evaluate((element) => {
    const rgb = (value: string) => value.match(/[\d.]+/g)!.map(Number);
    const lum = (colour: number[]) => colour.slice(0, 3).map((value) => {
      let channel = value / 255;
      return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
    }).reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
    const ancestors: Element[] = [];
    for (let node: Element | null = element; node; node = node.parentElement) ancestors.push(node);
    let background = [255, 255, 255];
    for (const node of ancestors.reverse()) {
      const colour = rgb(getComputedStyle(node).backgroundColor);
      const alpha = colour[3] ?? 1;
      background = background.map((value, index) => colour[index] * alpha + value * (1 - alpha));
    }
    const foreground = rgb(getComputedStyle(element).color);
    const alpha = foreground[3] ?? 1;
    const blended = foreground.slice(0, 3).map((value, index) => value * alpha + background[index] * (1 - alpha));
    const [low, high] = [lum(blended), lum(background)].sort((a, b) => a - b);
    return (high + 0.05) / (low + 0.05);
  });
}

const noHorizontalOverflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);

test('mobile navigation, practice, AI drawer and dark theme fit the viewport', async ({page}) => {
  await signInIsolatedLearner(page, 'mobile-layout');
  await page.setViewportSize({width: 390, height: 844});
  await page.goto('/');
  await expect(page.locator('.dashboard')).toBeVisible();
  await page.screenshot({path: 'test-results/dashboard-mobile.png', fullPage: true, animations: 'disabled'});
  expect(await noHorizontalOverflow(page)).toBe(true);

  await page.getByRole('button', {name: '打开导航', exact: true}).click();
  await page.getByRole('navigation', {name: '主导航'}).getByText('题库练习').click();
  await page.getByRole('button', {name: '开始练习', exact: true}).click();
  await page.getByRole('radio').first().click();
  await page.getByRole('button', {name: '提交答案', exact: true}).click();
  await expect(page.locator('.answer-panel')).toBeVisible();
  expect(await noHorizontalOverflow(page)).toBe(true);
  await page.screenshot({path: 'test-results/practice-mobile.png', fullPage: true, animations: 'disabled'});

  await page.getByRole('button', {name: '问问 AI', exact: true}).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  expect(await noHorizontalOverflow(page)).toBe(true);
  await page.getByRole('button', {name: '关闭 AI 辅导', exact: true}).click();

  await page.getByRole('button', {name: '切换深色模式', exact: true}).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.screenshot({path: 'test-results/practice-mobile-dark.png', fullPage: true, animations: 'disabled'});
  expect(await noHorizontalOverflow(page)).toBe(true);
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
});

test('saved dark palette is applied before content and remains readable', async ({page}) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem('rka-theme')) localStorage.setItem('rka-theme', 'dark');
  });
  await signIn(page);
  await page.goto('/');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('html')).toHaveCSS('background-color', 'rgb(23, 30, 27)');
  await expect(page.locator('.dashboard')).toBeVisible();
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(23, 30, 27)');
  for (const selector of ['h1', '.brand strong', '.daily-ring strong', '.stat-card>strong', '.path-row h3', '.hero-copy h2', '.hero-button', '.block-front strong']) {
    expect(await contrast(page, selector), selector).toBeGreaterThanOrEqual(4.5);
  }
  await page.getByRole('button', {name: '切换浅色模式', exact: true}).click();
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(246, 247, 245)');
});

test('dark reading, tables, editor and AI drawer keep readable foregrounds', async ({page}) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem('rka-theme')) localStorage.setItem('rka-theme', 'dark');
  });
  await signIn(page);
  const catalog = await catalogOf(page);
  const material = findMaterial(catalog, '第6章');
  await page.goto(`/knowledge?id=${material.id}`);
  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  for (const selector of ['.source-document>.markdown p', '.source-document>.markdown th', '.source-document>.markdown td', '.toc-scroll button', '.reader-tools .button.primary']) {
    expect(await contrast(page, selector), selector).toBeGreaterThanOrEqual(4.5);
  }
  await page.getByRole('button', {name: 'AI 讲解 / 解释选中文本', exact: true}).click();
  for (const selector of ['.ai-title', '.ai-description', '.ai-welcome h3', '.prompt-list button', '.chat-composer textarea']) {
    expect(await contrast(page, selector), selector).toBeGreaterThanOrEqual(4.5);
  }
  await expect(page.locator('.ai-panel')).toHaveCSS('background-color', 'rgb(32, 41, 37)');
  await page.getByRole('button', {name: '关闭 AI 辅导', exact: true}).click();

  await page.goto('/essays');
  await page.locator('.writing-card').first().click();
  await expect(page.locator('.essay-textarea')).toBeVisible();
  expect(await contrast(page, '.essay-textarea')).toBeGreaterThanOrEqual(4.5);
  await expect(page.locator('.essay-textarea')).toHaveCSS('background-color', 'rgb(32, 41, 37)');

  await page.goto('/settings');
  await expect(page.getByRole('heading', {name: 'AI 学习搭子'})).toBeVisible();
  expect(await contrast(page, '.settings-form input')).toBeGreaterThanOrEqual(4.5);
});

test('Mermaid diagrams are redrawn when the theme changes either way', async ({page}) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem('rka-theme')) localStorage.setItem('rka-theme', 'dark');
  });
  await signIn(page);
  const catalog = await catalogOf(page);
  const material = findMaterial(catalog, '第7章');
  await page.goto(`/knowledge?id=${material.id}`);
  const diagram = page.locator('.diagram-host').first();
  await diagram.evaluate((element) => window.scrollTo({top: element.getBoundingClientRect().top + scrollY - 200, behavior: 'instant'}));
  await expect(diagram).toHaveAttribute('data-diagram-theme', 'dark', {timeout: 30_000});
  const darkFill = await diagram.locator('.node rect').first().evaluate((element) => getComputedStyle(element).fill);
  expect(darkFill).toBe('rgb(43, 61, 50)');
  await expect(diagram.locator('.diagram')).toHaveCSS('background-color', 'rgb(38, 48, 43)');

  await page.getByRole('button', {name: '切换浅色模式', exact: true}).click();
  await expect(diagram).toHaveAttribute('data-diagram-theme', 'light', {timeout: 30_000});
  expect(await diagram.locator('.node rect').first().evaluate((element) => getComputedStyle(element).fill)).toBe('rgb(237, 244, 238)');

  await page.getByRole('button', {name: '切换深色模式', exact: true}).click();
  await expect(diagram).toHaveAttribute('data-diagram-theme', 'dark', {timeout: 30_000});
  expect(await diagram.locator('.node rect').first().evaluate((element) => getComputedStyle(element).fill)).toBe(darkFill);
});

test('mobile dark exercise keeps chosen, correct and incorrect states legible', async ({page, request}) => {
  await signInIsolatedLearner(page, 'mobile-dark');
  const admin = await adminApi(request);
  await page.setViewportSize({width: 390, height: 844});
  await page.addInitScript(() => localStorage.setItem('rka-theme', 'dark'));
  await page.goto('/practice');
  await page.getByRole('button', {name: '开始练习', exact: true}).click();
  await expect(page.getByRole('radio').first()).toBeVisible();
  const session = await (await page.request.get('/api/practice/sessions/active')).json();
  const questionId = session.session.questionIds[session.session.index];
  const solution = await solutionFor(admin, questionId);
  const wrong = (['A', 'B', 'C', 'D'] as const).find((option) => option !== solution?.answer)!;
  await page.getByRole('radio').nth(['A', 'B', 'C', 'D'].indexOf(wrong)).click();
  expect(await contrast(page, '.option.chosen .option-key')).toBeGreaterThanOrEqual(4.5);
  await page.getByRole('button', {name: '提交答案', exact: true}).click();
  await expect(page.locator('.answer-panel')).toBeVisible();
  await expect(page.locator('.option.incorrect')).toBeVisible();
  for (const selector of ['.option.correct', '.option.incorrect', '.option.incorrect .option-key', '.option.muted', '.answer-panel .markdown', '.question-navigation .button.primary']) {
    expect(await contrast(page, selector), selector).toBeGreaterThanOrEqual(4.5);
  }
  expect(await noHorizontalOverflow(page)).toBe(true);
});
