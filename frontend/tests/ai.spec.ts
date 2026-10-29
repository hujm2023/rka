// AI teaching tasks: explicit unavailability reasons, server-side task/SSE
// lifecycle (with mocked upstream events) and gateway configuration privacy.
import {expect, test} from '@playwright/test';
import {activeSession, catalogOf, chooseOption, signInIsolatedLearner, startPractice, submitAnswer} from './support/e2e';

const statusConfigured = {
  configured: true,
  gatewayName: 'Magpie',
  endpoint: 'http://127.0.0.1:3425/v1/chat/completions',
  model: 'group/deepseek-v4.1-flash-failover',
  authConfigured: true,
  connectionStatus: 'ok',
  capabilityStatus: 'verified',
  unavailableReason: '',
  teaching: {configured: true, reachable: true, reason: ''},
  budget: {dailyLimit: 0, usedToday: 0},
  concurrency: {limit: 3, active: 1},
};

const sse = (seq: number, type: string, payload: unknown) =>
  `event: task\nid: ${seq}\ndata: ${JSON.stringify({seq, type, payload})}\n\n`;

test('unavailable AI shows the reason and basic learning still works', async ({page}) => {
  // Exercise the degraded contract independently of the deployment's enabled services.
  await page.route('**/api/ai/status',route=>route.fulfill({json:{configured:false,unavailableReason:'未配置 AI 模型',teaching:{configured:false,reason:'教学服务尚未配置'},budget:{},concurrency:{}}}));
  await page.route('**/api/ai/tasks',route=>route.request().method()==='POST'?route.fulfill({status:503,json:{error:{code:'ai_unavailable',message:'未配置 AI 模型，基础学习功能不受影响。'}}}):route.continue());

  await signInIsolatedLearner(page, 'ai-unconfigured');
  await page.goto('/practice');
  await startPractice(page);

  await page.getByRole('button', {name: '给我一点提示', exact: true}).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.locator('.ai-unavailable')).toContainText('服务器 AI 暂不可用');
  await expect(page.locator('.ai-unavailable')).toContainText('未配置');
  await expect(page.locator('.ai-task-note')).toContainText('教学服务尚未配置');

  await page.getByRole('button', {name: '给我一个提示，先别告诉我答案', exact: true}).click();
  await expect(page.locator('.ai-panel .inline-error')).toContainText('基础学习功能不受影响');
  await page.getByRole('button', {name: '关闭 AI 辅导', exact: true}).click();

  // Practice remains fully usable without a model.
  await chooseOption(page, 'B');
  await submitAnswer(page);
  await expect(page.locator('.answer-panel')).toBeVisible();
  await expect(page.locator('.answer-panel')).not.toHaveClass(/pending/);

  await page.goto('/settings');
  await expect(page.locator('.ai-connection-settings')).toContainText('服务器尚未配置网关');
  await expect(page.locator('.ai-connection-settings .inline-warning').first()).toContainText('AI 暂不可用');
  await expect(page.locator('.ai-connection-settings')).toContainText('教学服务未配置');
});

test('server tasks stream citations, resume after a disconnect and keep the conversation', async ({page}) => {
  const account = await signInIsolatedLearner(page, 'ai-mock');
  expect(account.email).toContain('ai-mock');
  const catalog = await catalogOf(page);
  await page.goto('/practice');
  await startPractice(page);
  const session = await activeSession(page);
  const questionId = session.questionIds[0];
  const links = catalog.knowledgeLinks[questionId] || [];
  const link = links.find((entry: any) => entry.materialId && entry.sectionId);
  expect(link, 'linked source for the mock citation').toBeTruthy();

  let taskRequest: any = null;
  const eventCalls: string[] = [];
  await page.route('**/api/ai/status', (route) => route.fulfill({json: statusConfigured}));
  await page.route('**/api/ai/tasks', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    taskRequest = JSON.parse(route.request().postData() || '{}');
    return route.fulfill({status: 201, json: {task: {id: 'task-e2e', status: 'queued'}}});
  });
  await page.route('**/api/ai/tasks/task-e2e/events*', async (route) => {
    eventCalls.push(route.request().url());
    if (eventCalls.length === 1) {
      // First connection ends after one delta: the client must resume by seq.
      return route.fulfill({status: 200, contentType: 'text/event-stream', body: sse(1, 'delta', {text: '先分清题目'})});
    }
    return route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: sse(2, 'delta', {text: '中的主体与客体，再看访问控制矩阵。[S0]'})
        + sse(3, 'citation', {sourceId: 'S0', title: link.sectionTitle, materialId: link.materialId, sectionId: link.sectionId})
        + sse(4, 'done', {status: 'completed'}),
    });
  });

  await page.getByRole('button', {name: '给我一点提示', exact: true}).click();
  await page.getByRole('button', {name: '给我一个提示，先别告诉我答案', exact: true}).click();
  const assistant = page.locator('.chat-message.assistant').last();
  await expect(assistant).toContainText('访问控制矩阵');
  await expect(assistant).toContainText('先分清题目');
  await expect(assistant.locator('.citations')).toContainText('[S0]');
  const citationLink = assistant.locator('.citations a').first();
  const href = await citationLink.getAttribute('href');
  expect(href).toContain(link.materialId);
  expect(href).toContain(link.sectionId);

  // The task is created through the public contract: hint kind, linked question
  // and no reference answer in the context.
  expect(taskRequest.kind).toBe('hint');
  expect(taskRequest.idempotencyKey).toBeTruthy();
  expect(taskRequest.context.questionId).toBe(questionId);
  expect(taskRequest.context.hint).toBe(true);
  expect(JSON.stringify(taskRequest)).not.toContain('参考答案');
  expect(JSON.stringify(taskRequest)).not.toContain('analysis');

  // The stream resumed with the last acknowledged sequence number.
  expect(eventCalls.length).toBeGreaterThanOrEqual(2);
  expect(eventCalls[1]).toContain('after=1');

  // The conversation is cached and restored after a reload.
  await page.getByRole('button', {name: '关闭 AI 辅导', exact: true}).click();
  await page.reload();
  await expect(page.getByRole('radio').first()).toBeVisible();
  await page.getByRole('button', {name: '给我一点提示', exact: true}).click();
  await expect(page.locator('.chat-message.assistant').last()).toContainText('访问控制矩阵');
  await expect(page.locator('.chat-message.assistant').last().locator('.citations')).toContainText('[S0]');
});

test('broken event streams surface an explicit recoverable error', async ({page}) => {
  await signInIsolatedLearner(page, 'ai-broken');
  await page.goto('/practice');
  await startPractice(page);
  await page.route('**/api/ai/status', (route) => route.fulfill({json: statusConfigured}));
  await page.route('**/api/ai/tasks', (route) => route.fulfill({status: 201, json: {task: {id: 'task-broken', status: 'queued'}}}));
  await page.route('**/api/ai/tasks/task-broken/events*', (route) => route.fulfill({status: 500, json: {error: {code: 'internal', message: 'event stream unavailable'}}}));

  await page.getByRole('button', {name: '给我一点提示', exact: true}).click();
  await page.getByRole('button', {name: '给我一个提示，先别告诉我答案', exact: true}).click();
  await expect(page.locator('.ai-panel .inline-error')).toContainText('事件流中断', {timeout: 30_000});
});

test('gateway metadata is displayed without exposing credentials and custom mode is explicit', async ({page}) => {
  await signInIsolatedLearner(page, 'ai-config');
  await page.route('**/api/ai/status', (route) => route.fulfill({json: statusConfigured}));
  await page.goto('/settings');
  const section = page.locator('.ai-connection-settings');
  await expect(section).toContainText('服务器网关已配置');
  await expect(section.locator('.gateway-summary strong')).toHaveText('Magpie');
  await expect(page.getByLabel('上游网关接口')).toHaveValue(statusConfigured.endpoint);
  await expect(page.getByLabel('上游网关接口')).toHaveAttribute('readonly', '');
  await expect(page.getByLabel('当前模型 / 路由组')).toHaveValue(statusConfigured.model);
  await expect(page.getByLabel('当前模型 / 路由组')).toHaveAttribute('readonly', '');
  await expect(section.locator('.gateway-auth')).toContainText('已在服务端配置');
  await expect(section.locator('.status-triplet')).toContainText('配置状态');
  await expect(section.locator('.status-triplet')).toContainText('连接状态');
  await expect(section.locator('.status-triplet')).toContainText('能力验证');
  await expect(section.locator('input[type=password]')).toHaveCount(0);
  await page.getByText('查看网站转发地址', {exact: true}).click();
  await expect(section.locator('.gateway-connection-details code')).toHaveText(new URL('/api/ai', page.url()).href);

  // Custom mode is an explicit, separate choice and never stores the key.
  await page.getByRole('tab', {name: '自定义接口', exact: true}).click();
  await expect(section).toContainText('切换尚未保存');
  await page.getByLabel('完整接口地址').fill('https://example.test/v1/chat/completions');
  await page.getByLabel('模型名称').fill('custom-model');
  await page.getByLabel('API Key').fill('browser-private-key');
  await page.getByRole('button', {name: '保存自定义接口', exact: true}).click();
  await expect(section).toContainText('当前使用自定义接口');
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain('browser-private-key');
  await page.reload();
  await expect(page.getByLabel('模型名称')).toHaveValue('custom-model');
  await expect(page.getByLabel('API Key')).toHaveValue('');
  await page.getByRole('tab', {name: '使用服务器网关', exact: true}).click();
  await page.getByRole('button', {name: '使用此网关', exact: true}).click();
  const me=await (await page.request.get('/api/auth/me')).json();
  expect(await page.evaluate(id=>JSON.parse(localStorage.getItem(`rka-ai-settings:${id}`)!),me.user.id)).toEqual({endpoint: '/api/ai', model: ''});
  await page.reload();
  await expect(section).toContainText('服务器网关已配置');
});

test('gateway read failures are explicit and fit a dark mobile screen', async ({page}) => {
  await signInIsolatedLearner(page, 'ai-config-error');
  await page.setViewportSize({width: 390, height: 844});
  await page.addInitScript(() => localStorage.setItem('rka-theme', 'dark'));
  await page.route('**/api/ai/status', (route) => route.fulfill({status: 503, body: 'unavailable'}));
  await page.goto('/settings');
  const section = page.locator('.ai-connection-settings');
  await expect(section).toContainText('网关配置读取失败');
  await expect(section.locator('.inline-error')).toContainText('读取服务器配置失败');
  await page.unroute('**/api/ai/status');
  await page.route('**/api/ai/status',route=>route.fulfill({json:{configured:false,endpoint:'',model:'',authConfigured:false,unavailableReason:'未配置 AI',teaching:{configured:false},budget:{},concurrency:{}}}));
  await page.getByRole('button', {name: '刷新配置', exact: true}).click();
  await expect(section).toContainText('服务器尚未配置网关');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
