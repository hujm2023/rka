// Practice, exam and cross-device behaviour. Every case drives the real UI and
// then checks the authoritative server state through the public API.
import {expect, test} from '@playwright/test';
import {
  activeSession, adminApi, catalogOf, chooseOption, findMaterial, getJson, openDevice,
  signIn, signInIsolatedLearner, solutionFor, startPractice, submitAnswer, type Option,
} from './support/e2e';

test('answer selection, submission, position and bookmarks survive reload', async ({page}) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await signInIsolatedLearner(page, 'practice-reload');
  await page.goto('/practice');
  await startPractice(page);

  // Choosing by click and by keyboard; the choice is not submitted yet.
  await chooseOption(page, 'B');
  await page.keyboard.press('c');
  await expect(page.getByRole('radio').nth(2)).toHaveAttribute('aria-checked', 'true');
  await page.keyboard.press('b');
  await expect(page.getByRole('radio').nth(1)).toHaveAttribute('aria-checked', 'true');

  // The requirement is explicit: an unsubmitted choice survives a refresh.
  await page.reload();
  await expect(page.getByRole('radio').nth(1)).toHaveAttribute('aria-checked', 'true');

  const beforeSubmit = await activeSession(page);
  const firstQuestion = beforeSubmit.questionIds[beforeSubmit.index];
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toBeVisible();
  await expect(page.locator('.answer-panel')).not.toHaveClass(/pending/);

  // The server recorded exactly one answer with the reference solution.
  const submitted = await activeSession(page);
  const state = submitted.answers[firstQuestion];
  expect(state.submitted).toBe(true);
  expect(state.revealed).toBe(true);
  expect(state.solution?.answer).toMatch(/^[A-D]$/);
  expect(state.correct).toBe(state.selected === state.solution?.answer);
  const attempts = await getJson<{attempts?: any[]; items?: any[]}>(page, `/api/practice/attempts?questionId=${encodeURIComponent(firstQuestion)}`);
  expect((attempts.attempts || attempts.items || []).length).toBe(1);

  // Bookmarks are server facts and survive a reload.
  await page.getByRole('button', {name: '收藏题目', exact: true}).click();
  await expect(page.getByRole('button', {name: '已收藏', exact: true})).toBeVisible();
  const marks = await getJson<{items?: any[]}>(page, '/api/practice/marks');
  expect((marks.items || []).find((row) => row.id === firstQuestion)?.favorite).toBe(true);

  // Moving on persists the index; the next unsubmitted selection survives too.
  await page.getByRole('button', {name: '下一题', exact: true}).click();
  await chooseOption(page, 'B');
  await page.reload();
  await expect(page.locator('.question-topline')).toContainText('2');
  await expect(page.getByRole('radio').nth(1)).toHaveAttribute('aria-checked', 'true');

  await page.getByRole('button', {name: '上一题', exact: true}).click();
  await expect(page.locator('.answer-panel')).toBeVisible();
  await expect(page.getByRole('button', {name: '已收藏', exact: true})).toBeVisible();
  expect(errors).toEqual([]);
});

test('chapter practice only queues linked questions and a wrong answer returns to its exact source', async ({page, request}) => {
  await signInIsolatedLearner(page, 'practice-chapter');
  const admin = await adminApi(request);
  const catalog = await catalogOf(page);
  const material = findMaterial(catalog, '第6章');

  await page.goto(`/knowledge?id=${material.id}`);
  await page.getByRole('link', {name: /^练本章（/}).click();
  await expect(page.getByRole('radio').first()).toBeVisible();
  await expect(page).toHaveURL(/\/practice$/);

  const session = await activeSession(page);
  expect(session.scope.materialId).toBe(material.id);
  expect(session.questionIds.length).toBeGreaterThan(0);
  for (const questionId of session.questionIds) {
    const links = catalog.knowledgeLinks[questionId] || [];
    expect(links.some((link: any) => link.materialId === material.id), `question ${questionId} must belong to the chapter`).toBe(true);
  }

  // Administrators can read the solution before submission, so the test can
  // deliberately produce a wrong answer through the real UI.
  const questionId = session.questionIds[session.index];
  const solution = await solutionFor(admin, questionId);
  expect(solution?.answer).toMatch(/^[A-D]$/);
  const wrong = (['A', 'B', 'C', 'D'] as Option[]).find((option) => option !== solution!.answer)!;
  await chooseOption(page, wrong);
  await submitAnswer(page);
  await expect(page.locator('.answer-panel.failure')).toBeVisible();

  const source = page.locator('.answer-panel .knowledge-source-link').first();
  const href = await source.getAttribute('href');
  expect(href).toBeTruthy();
  const sectionId = new URL(href!, 'http://localhost').searchParams.get('section');
  expect(sectionId).toBeTruthy();
  await source.click();

  await expect(page.locator('.source-document>.markdown')).toBeVisible();
  await expect(page.getByRole('link', {name: '返回本题继续练习', exact: true})).toBeVisible();
  await expect.poll(async () => page.locator(`[id="${sectionId}"]`).evaluate((element) => Math.round(element.getBoundingClientRect().top))).toBeLessThan(240);

  await page.getByRole('link', {name: '返回本题', exact: true}).click();
  await expect(page.locator('.answer-panel.failure')).toBeVisible();
  const resumed = await activeSession(page);
  expect(resumed.id).toBe(session.id);
  expect(resumed.questionIds).toEqual(session.questionIds);
  expect(resumed.index).toBe(session.index);
  expect(resumed.answers[questionId].submitted).toBe(true);
});

test('switching to another chapter keeps the unfinished session until confirmed', async ({page}) => {
  await signInIsolatedLearner(page, 'practice-scope');
  const catalog = await catalogOf(page);
  const first = findMaterial(catalog, '第6章');
  const second = findMaterial(catalog, '第8章');

  await page.goto(`/knowledge?id=${first.id}`);
  await page.getByRole('link', {name: /^练本章（/}).click();
  await page.getByRole('radio').first().click();
  const before = await activeSession(page);
  expect(before.scope.materialId).toBe(first.id);

  // Starting a chapter with a different scope must ask before replacing.
  await page.goto(`/knowledge?id=${second.id}`);
  await page.getByRole('link', {name: /^练本章（/}).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('dialog')).toContainText('开始一组新练习？');
  await page.getByRole('button', {name: '继续保留', exact: true}).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();

  const after = await activeSession(page);
  expect(after.id).toBe(before.id);
  expect(after.questionIds).toEqual(before.questionIds);
  expect(after.answers).toEqual(before.answers);
  expect(after.index).toBe(before.index);
});

test('exam hides answers before submission and settles the score on submit', async ({page}) => {
  await signInIsolatedLearner(page, 'practice-exam');
  await page.goto('/practice?mode=exam');

  const term = page.getByLabel('考试年份 / 套卷');
  await expect(term).toBeVisible();
  const start = page.getByRole('button', {name: '开始练习', exact: true});
  await expect(start).toBeDisabled();
  const terms = await term.locator('option').evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value));
  const chosenTerm = terms.find((value) => value && value !== 'all');
  expect(chosenTerm).toBeTruthy();
  await term.selectOption(chosenTerm!);
  await expect(start).toBeEnabled();
  await start.click();

  await expect(page.locator('.exam-banner')).toContainText('交卷前不公布答案');
  const session = await activeSession(page);
  expect(session.mode).toBe('exam');
  expect(session.deadlineAt).toBeTruthy();
  const questionId = session.questionIds[0];

  await chooseOption(page, 'A');
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toBeVisible();
  await expect(page.locator('.answer-panel')).toContainText('答案已记录，交卷后公布结果');
  await expect(page.locator('.option.correct')).toHaveCount(0);

  // Server state: recorded but not revealed; the solution endpoint withholds it.
  const submitted = await activeSession(page);
  const answer = submitted.answers[questionId];
  expect(answer.submitted).toBe(true);
  expect(answer.revealed).toBe(false);
  expect(answer.solution).toBeUndefined();
  expect(submitted.score).toBeNull();
  const hidden = await page.request.get(`/api/content/questions/${encodeURIComponent(questionId)}/solution`);
  expect(hidden.ok()).toBeTruthy();
  const hiddenBody = await hidden.json();
  expect(hiddenBody.answerHidden).toBe(true);
  expect(hiddenBody.answer).toBeUndefined();

  await page.getByRole('button', {name: '提前交卷', exact: true}).click();
  await expect(page.getByRole('dialog')).toContainText('交卷后立即由服务器评分');
  await page.getByRole('button', {name: '确认交卷', exact: true}).click();
  await expect(page.locator('.exam-result')).toBeVisible();
  await expect(page.locator('.exam-result')).toContainText('服务器评分');

  const finished = await getJson<any>(page, `/api/practice/sessions/${session.id}`);
  expect(finished.session.status).not.toBe('active');
  expect(finished.session.submittedAt).toBeTruthy();
  expect(typeof finished.session.score).toBe('number');
  expect(finished.session.maxScore).toBeGreaterThan(0);
  expect(finished.session.score).toBeLessThanOrEqual(finished.session.maxScore);

  // After submission the UI reveals the reference answer again.
  await expect(page.locator('.option.correct').first()).toBeVisible();
  const solution = await page.request.get(`/api/content/questions/${encodeURIComponent(questionId)}/solution`);
  expect(solution.ok()).toBeTruthy();
  expect((await solution.json()).answer).toMatch(/^[A-D]$/);
});

test('a second device restores the same session, question and submitted answer', async ({page, browser}) => {
  const account = await signInIsolatedLearner(page, 'practice-device');
  await page.goto('/practice');
  await startPractice(page);

  const first = await activeSession(page);
  const firstQuestion = first.questionIds[first.index];
  await chooseOption(page, 'A');
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toBeVisible();
  await page.getByRole('button', {name: '下一题', exact: true}).click();
  await expect(page.locator('.question-topline')).toContainText('2');

  const server = await activeSession(page);
  expect(server.index).toBe(1);

  const second = await openDevice(browser);
  try {
    await signIn(second.page, account);
    await second.page.goto('/practice');
    await expect(second.page.locator('.question-topline')).toContainText('2');
    const restored = await activeSession(second.page);
    expect(restored.id).toBe(server.id);
    expect(restored.index).toBe(1);
    expect(restored.answers[firstQuestion].submitted).toBe(true);

    await second.page.getByRole('button', {name: '上一题', exact: true}).click();
    await expect(second.page.locator('.answer-panel')).toBeVisible();
    await expect(second.page.locator('.answer-panel')).not.toHaveClass(/pending/);
  } finally {
    await second.context.close();
  }
});

test('practice works over plain HTTP when crypto.randomUUID is unavailable', async ({page}) => {
  // randomUUID only exists in secure contexts; the LAN preview is plain HTTP.
  await page.addInitScript(() => Object.defineProperty(window.crypto, 'randomUUID', {value: undefined, configurable: true}));
  await signInIsolatedLearner(page, 'practice-http');
  await page.goto('/practice?module=architecture');
  await page.getByRole('button', {name: '开始练习', exact: true}).click();
  await expect(page).toHaveURL(/\/practice$/);
  await chooseOption(page, 'A');
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toBeVisible();
  const session = await activeSession(page);
  expect(session.answers[session.questionIds[session.index]].submitted).toBe(true);
  await page.getByRole('button', {name: '下一题', exact: true}).click();
  await page.reload();
  await expect(page.locator('.question-topline')).toContainText('2');
  await expect(page.getByRole('radio').first()).toBeVisible();
});

test('a delayed position response preserves a newly selected answer through reload', async ({page}) => {
  await signInIsolatedLearner(page, 'delayed-position');
  await page.goto('/practice');
  await startPractice(page);
  await chooseOption(page, 'B');
  await submitAnswer(page);
  let release!: () => void;
  let captured!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const requested = new Promise<void>(resolve => { captured = resolve; });
  let held = false;
  await page.route('**/api/practice/sessions/*', async route => {
    if (route.request().method() !== 'PATCH' || held) return route.continue();
    held = true;
    const response = await route.fetch();
    captured();
    await gate;
    await route.fulfill({response});
  });
  await page.getByRole('button', {name: '下一题', exact: true}).click();
  await requested;
  await chooseOption(page, 'D');
  const response = page.waitForResponse(r => r.request().method() === 'PATCH' && r.url().includes('/api/practice/sessions/'));
  release();
  await response;
  await expect(page.getByRole('radio').nth(3)).toHaveAttribute('aria-checked', 'true');
  await page.reload();
  await expect(page.locator('.question-topline')).toContainText('2');
  await expect(page.getByRole('radio').nth(3)).toHaveAttribute('aria-checked', 'true');
});
