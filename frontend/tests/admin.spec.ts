// Administration console: status separation, available operations and the
// boundary between administrators and learners.
import {expect, test} from '@playwright/test';
import {adminEmail, signIn} from './support/auth';
import {signInIsolatedLearner} from './support/e2e';

test('administrators can open the console and every status is shown separately', async ({page}) => {
  await signIn(page);
  await page.goto('/');
  await expect(page.getByRole('navigation', {name: '主导航'}).getByText('管理后台')).toBeVisible();
  await page.goto('/admin');
  await expect(page.getByRole('heading', {name: '平台管理', exact: true})).toBeVisible();
  await expect(page.getByRole('tab', {name: '状态', exact: true})).toBeVisible();

  // Health dimensions are separate cards with their own state, not one boolean.
  const health = page.locator('.health-grid');
  await expect(page.locator('.health-card')).toHaveCount(5);
  for (const key of ['process', 'database', 'teaching', 'model', 'index']) {
    await expect(health).toContainText(key);
  }
  await expect(health).toContainText('ok');
  const currentStatus=await (await page.request.get('/api/admin/status')).json();
  for(const [key,value] of Object.entries(currentStatus.checks)){await expect(page.locator('.health-card').filter({hasText:key})).toContainText(String(value));}

  // Model configuration separates configured / connection / capability status.
  // A config is created through the public admin API so the three states are
  // rendered independently instead of being collapsed into one flag.
  const created = await page.request.post('/api/admin/model-configs', {
    data: {name: 'E2E 测试模型', kind: 'llm', provider: 'openai-compatible', endpoint: 'http://127.0.0.1:1/v1/chat/completions', model: 'e2e-model'},
  });
  expect(created.ok()).toBeTruthy();
  await page.getByRole('tab', {name: '模型配置', exact: true}).click();
  await expect(page.locator('.environment-strip')).toContainText('服务器默认环境');
  const modelConfigs=await (await page.request.get('/api/admin/model-configs')).json();
  await expect(page.locator('.environment-strip')).toContainText(modelConfigs.environment.configured?'已配置':'未配置');
  await expect(page.locator('.model-card').first()).toBeVisible();
  const statuses = page.locator('.status-triplet').first();
  await expect(statuses).toContainText('配置状态');
  await expect(statuses).toContainText('已配置');
  await expect(statuses).toContainText('连接状态');
  await expect(statuses).toContainText('unknown');
  await expect(statuses).toContainText('能力验证');
  await expect(statuses).toContainText('unverified');

  await page.getByRole('tab', {name: '用户与邀请', exact: true}).click();
  await expect(page.locator('.admin-table tbody tr').first()).toBeVisible();
  await expect(page.locator('.admin-table')).toContainText(adminEmail);
  await expect(page.getByRole('button', {name: '创建账号', exact: true})).toBeVisible();
  await expect(page.getByRole('button', {name: '生成邀请令牌', exact: true})).toBeVisible();

  await page.getByRole('tab', {name: '任务', exact: true}).click();
  await expect(page.getByRole('heading', {name: '后台任务', exact: true})).toBeVisible();
  await page.getByRole('tab', {name: '审计', exact: true}).click();
  await expect(page.getByRole('heading', {name: '审计日志', exact: true})).toBeVisible();
  await page.getByRole('tab', {name: '备份与保留', exact: true}).click();
  await expect(page.getByRole('button', {name: '创建一致性备份', exact: true})).toBeVisible();
  await expect(page.getByLabel('任务保留天数', {exact: true})).toBeVisible();
});

test('learners cannot see or call the administration console', async ({page}) => {
  await signInIsolatedLearner(page, 'admin-denied');
  await expect(page.getByRole('navigation', {name: '主导航'}).getByText('管理后台')).toHaveCount(0);
  await page.goto('/admin');
  await expect(page.getByText('没有访问权限', {exact: true})).toBeVisible();
  await expect(page.getByRole('heading', {name: '平台管理', exact: true})).toHaveCount(0);
  const response = await page.request.get('/api/admin/status');
  expect(response.status()).toBe(403);
});
