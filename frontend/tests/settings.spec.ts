// Settings and data ownership: backup validation, import idempotency and the
// four sync states shown to the user (local/pending/synced/conflict).
import {expect, test} from '@playwright/test';
import fs from 'node:fs/promises';
import {activeSession, catalogOf, chooseOption, getJson, signIn, signInApi, signInIsolatedLearner, startPractice, submitAnswer} from './support/e2e';

test('backup export is complete, malformed imports are rejected and valid imports are idempotent', async ({page}) => {
  const account = await signInIsolatedLearner(page, 'settings-backup');
  const catalog = await catalogOf(page);
  const questionId = catalog.questions[0].id;
  await page.goto('/settings');

  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', {name: /导出完整备份/}).click();
  const download = await downloadPromise;
  const backupText = await fs.readFile((await download.path())!, 'utf8');
  const backup = JSON.parse(backupText);
  expect(backup.app).toBe('rka');
  expect(backup.schemaVersion).toBeGreaterThanOrEqual(1);
  expect(backup.counts).toBeTruthy();
  expect(backupText).not.toContain('apiKey');
  expect(backupText).not.toContain('password');

  // A structurally incomplete v1 backup must be rejected before it reaches the
  // server, with a reason and without touching the stored data.
  await page.locator('input[type=file]').setInputFiles({
    name: 'broken.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({app: 'rka', schemaVersion: 1, data: {attempts: []}})),
  });
  await expect(page.locator('.settings-page .inline-error')).toContainText('数据不完整');
  const exportedBefore = await getJson<any>(page, '/api/export/me');
  expect(exportedBefore.counts.attempts).toBe(0);

  // A valid browser v1 backup is imported by the server, idempotently.
  const legacyBackup = {
    app: 'rka',
    schemaVersion: 1,
    exportedAt: '2026-10-20T00:00:00Z',
    data: {
      attempts: [{
        id: 'e2e-import-attempt-1', questionId, revision: 'legacy', answer: 'A',
        correct: false, assisted: false, createdAt: '2026-10-20T10:00:00Z', durationMs: 1200,
      }],
      marks: [],
      sessions: [],
      drafts: [],
      reading: [],
      chats: [],
    },
  };
  const upload = async () => {
    await page.locator('input[type=file]').setInputFiles({
      name: 'backup.json',
      mimeType: 'application/json',
      buffer: Buffer.from(JSON.stringify(legacyBackup)),
    });
    await expect(page.getByRole('dialog')).toBeVisible();
    await expect(page.getByRole('dialog')).toContainText('1 条作答');
  };
  await upload();
  await page.getByRole('button', {name: '确认恢复', exact: true}).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.locator('.toast')).toContainText('备份已导入');
  await expect.poll(async () => (await getJson<any>(page, '/api/export/me')).counts.attempts).toBe(1);

  // Replaying the same backup does not duplicate the attempt.
  await upload();
  await page.getByRole('button', {name: '确认恢复', exact: true}).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect.poll(async () => (await getJson<any>(page, '/api/export/me')).counts.attempts).toBe(1);

  // The server export itself can be re-imported into the same account: the
  // operation is idempotent and reports the data it kept.
  await page.locator('input[type=file]').setInputFiles({
    name: 'server-export.json',
    mimeType: 'application/json',
    buffer: Buffer.from(backupText),
  });
  await expect(page.getByRole('dialog')).toContainText('服务器导出数据');
  await page.getByRole('button', {name: '确认恢复', exact: true}).click();
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.locator('.toast')).toContainText('备份已导入');
  const exportedAfter = await getJson<any>(page, '/api/export/me');
  expect(exportedAfter.counts.attempts).toBe(1);
  expect(account.email).toContain('settings-backup');
});

test('offline answers are queued as pending, then sync idempotently; stale marks surface a conflict', async ({page, browser, request}) => {
  const account = await signInIsolatedLearner(page, 'settings-sync');
  const api = await signInApi(request, account);
  await page.goto('/practice');
  await startPractice(page);
  const session = await activeSession(page);
  const questionId = session.questionIds[0];

  // ---- synced ---------------------------------------------------------
  await expect(page.locator('[aria-label="同步状态：已同步"]').first()).toBeVisible();

  // ---- pending (offline) ----------------------------------------------
  await chooseOption(page, 'A');
  await page.context().setOffline(true);
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toContainText('已保存到本机，等待同步');
  await expect(page.locator('.toast')).toContainText('待同步队列');
  await expect(page.locator('.sync-banner')).toContainText('网络不可用');
  await expect(page.locator('[aria-label="同步状态：待同步"]').first()).toBeVisible();
  const offlineServer = await (await api.get('/api/practice/sessions/active')).json();
  expect(offlineServer.session.answers?.[questionId]).toBeUndefined();

  // ---- synced again ---------------------------------------------------
  await page.context().setOffline(false);
  await expect(page.locator('[aria-label="同步状态：已同步"]').first()).toBeVisible({timeout: 20_000});
  await expect(page.locator('.sync-banner')).toBeHidden();
  await expect(page.locator('.answer-panel')).not.toContainText('等待同步');
  await expect.poll(async () => {
    const body = await (await api.get('/api/practice/sessions/active')).json();
    return body.session?.answers?.[questionId]?.submitted === true;
  }, {timeout: 20_000}).toBe(true);
  const attempts = await (await api.get(`/api/practice/attempts?questionId=${encodeURIComponent(questionId)}`)).json();
  expect((attempts.attempts || attempts.items || []).length).toBe(1);

  // ---- conflict ------------------------------------------------------
  // A second device updates the mark so this device's cached version is stale.
  await page.getByRole('button', {name: '收藏题目', exact: true}).click();
  await expect(page.getByRole('button', {name: '已收藏', exact: true})).toBeVisible();

  const second = await browser.newContext({baseURL: process.env.RKA_E2E_BASE_URL || 'http://127.0.0.1:4199', viewport: {width: 1440, height: 1000}});
  try {
    const other = await second.newPage();
    await signIn(other, account);
    await other.goto('/practice');
    await expect(other.getByRole('button', {name: '已收藏', exact: true})).toBeVisible();
    await other.getByRole('button', {name: '已收藏', exact: true}).click();
    await expect(other.getByRole('button', {name: '收藏题目', exact: true})).toBeVisible();

    // Offline edit built on the stale mark version, then replayed online.
    await page.context().setOffline(true);
    await page.getByRole('button', {name: '标记存疑', exact: true}).click();
    await expect(page.locator('[aria-label="同步状态：待同步"]').first()).toBeVisible();
    await page.context().setOffline(false);

    await expect(page.locator('[aria-label="同步状态：有冲突"]').first()).toBeVisible({timeout: 20_000});
    await expect(page.locator('.sync-banner')).toContainText('与服务器版本冲突');
    const discard = page.locator('.sync-conflicts').getByRole('button', {name: '放弃本地更改'});
    await expect(discard).toBeVisible();
    await discard.click();
    await expect(page.locator('[aria-label="同步状态：已同步"]').first()).toBeVisible({timeout: 20_000});
    await expect(page.locator('.sync-banner')).toBeHidden();

    const marks = await getJson<{items?: any[]}>(page, '/api/practice/marks');
    const mark = (marks.items || []).find((row) => row.id === questionId);
    expect(mark?.favorite).toBe(false);
    expect(mark?.uncertain).toBe(false);
    expect(mark?.version).toBe(2);
  } finally {
    await second.close();
  }
});
