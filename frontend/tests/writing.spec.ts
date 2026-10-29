// Case and essay writing: server-backed drafts, version history and an
// explicit conflict resolution that keeps both versions.
import {expect, test} from '@playwright/test';
import {getJson, openDevice, signIn, signInIsolatedLearner} from './support/e2e';

const targetIdOf = (page: any) => new URL(page.url()).searchParams.get('id')!;

test('case and essay drafts are durable and stored on the server', async ({page}) => {
  await signInIsolatedLearner(page, 'writing-drafts');

  await page.goto('/cases');
  await page.locator('.writing-card').first().click();
  const caseId = targetIdOf(page);
  const caseInput = page.getByRole('textbox', {name: '问题 1 的回答', exact: true});
  await caseInput.fill('采用分层架构，将业务逻辑与数据访问隔离。');
  await expect(page.locator('.page-heading .save-indicator')).toContainText('已保存到服务器');
  const savedCase = await getJson<any>(page, `/api/writing/case/${encodeURIComponent(caseId)}`);
  expect(savedCase.document.version).toBeGreaterThanOrEqual(1);
  expect(savedCase.document.values['answer-0']).toBe('采用分层架构，将业务逻辑与数据访问隔离。');
  await page.reload();
  await expect(caseInput).toHaveValue('采用分层架构，将业务逻辑与数据访问隔离。');

  await page.goto('/essays');
  await page.locator('.writing-card').first().click();
  const essayId = targetIdOf(page);
  await page.getByRole('textbox', {name: '项目素材', exact: true}).fill('我负责订单系统的架构设计，重点处理交易一致性。');
  await page.getByRole('tab', {name: '正文', exact: true}).click();
  await page.getByRole('textbox', {name: '正文', exact: true}).fill('我们使用事务消息，设计幂等消费和补偿机制。');
  await expect(page.locator('.page-heading .save-indicator')).toContainText('已保存到服务器');
  const savedEssay = await getJson<any>(page, `/api/writing/essay/${encodeURIComponent(essayId)}`);
  expect(savedEssay.document.values.project).toBe('我负责订单系统的架构设计，重点处理交易一致性。');
  expect(savedEssay.document.values.body).toBe('我们使用事务消息，设计幂等消费和补偿机制。');

  await page.reload();
  await expect(page.getByRole('textbox', {name: '项目素材', exact: true})).toHaveValue('我负责订单系统的架构设计，重点处理交易一致性。');
  await page.getByRole('tab', {name: '正文', exact: true}).click();
  await expect(page.getByRole('textbox', {name: '正文', exact: true})).toHaveValue('我们使用事务消息，设计幂等消费和补偿机制。');

  // The two draft kinds are independent documents.
  const after = await getJson<any>(page, `/api/writing/case/${encodeURIComponent(caseId)}`);
  expect(after.document.values['answer-0']).toBe('采用分层架构，将业务逻辑与数据访问隔离。');
});

test('concurrent edits raise a conflict dialog and both versions stay recoverable', async ({page, browser}) => {
  const account = await signInIsolatedLearner(page, 'writing-conflict');
  await page.goto('/essays');
  await page.locator('.writing-card').first().click();
  const essayId = targetIdOf(page);
  const first = page.getByRole('textbox', {name: '正文', exact: true});
  await page.getByRole('tab', {name: '正文', exact: true}).click();
  await first.fill('第一版正文来自设备A');
  await expect(page.locator('.page-heading .save-indicator')).toContainText('已保存到服务器');

  const second = await openDevice(browser);
  try {
    await signIn(second.page, account);
    await second.page.goto(`/essays?id=${encodeURIComponent(essayId)}`);
    const secondBody = second.page.getByRole('textbox', {name: '正文', exact: true});
    await second.page.getByRole('tab', {name: '正文', exact: true}).click();
    await expect(secondBody).toHaveValue('第一版正文来自设备A');
    await secondBody.fill('第二版正文来自设备B');
    await expect(second.page.locator('.page-heading .save-indicator')).toContainText('已保存到服务器');
  } finally {
    await second.context.close();
  }

  // The first device still holds the stale version and now conflicts.
  await first.fill('设备A的并发修改');
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('写作版本冲突');
  await expect(dialog).toContainText('设备A的并发修改');
  await expect(dialog).toContainText('第二版正文来自设备B');

  // Both sides are kept on the server.
  const versions = await getJson<any>(page, `/api/writing/essay/${encodeURIComponent(essayId)}/versions`);
  expect(versions.currentVersion).toBe(2);
  expect(versions.items.some((entry: any) => entry.source === 'conflict'), 'conflict version is kept').toBe(true);

  await page.getByRole('button', {name: '使用服务器版本', exact: true}).click();
  await expect(dialog).not.toBeVisible();
  await expect(first).toHaveValue('第二版正文来自设备B');
  const server = await getJson<any>(page, `/api/writing/essay/${encodeURIComponent(essayId)}`);
  expect(server.document.version).toBe(2);
  expect(server.document.values.body).toBe('第二版正文来自设备B');
});
