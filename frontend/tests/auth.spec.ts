// Browser session entry points: anonymous login, explicit errors and logout.
import {expect, test} from '@playwright/test';
import {adminEmail, adminPassword, type Account} from './support/auth';
import {baseURL} from './support/e2e';

const admin: Account = {email: adminEmail, password: adminPassword};

test('login page authenticates through the server cookie and logout clears it', async ({browser}) => {
  const context = await browser.newContext({baseURL, viewport: {width: 1440, height: 1000}});
  const page = await context.newPage();
  try {
    await page.goto('/');
    await expect(page.getByRole('heading', {name: '登录学习空间', exact: true})).toBeVisible();

    await page.getByLabel('邮箱', {exact: true}).fill('nobody@example.com');
    await page.getByLabel('密码', {exact: true}).fill('not-the-password');
    await page.getByRole('button', {name: '登录', exact: true}).click();
    await expect(page.getByRole('alert')).toContainText('邮箱或密码不正确');

    await page.getByLabel('邮箱', {exact: true}).fill(admin.email);
    await page.getByLabel('密码', {exact: true}).fill(admin.password);
    await page.getByRole('button', {name: '登录', exact: true}).click();
    await expect(page.locator('.dashboard')).toBeVisible();
    await expect(page.getByRole('navigation', {name: '主导航'}).getByText('学习概览')).toBeVisible();

    // An authenticated visitor no longer sees the login routes.
    await page.goto('/login');
    await expect(page.locator('.dashboard')).toBeVisible();

    await page.getByRole('button', {name: '退出登录', exact: true}).click();
    await expect(page.getByRole('heading', {name: '登录学习空间', exact: true})).toBeVisible();
    const me = await (await context.request.get('/api/auth/me')).json();
    expect(me.user).toBeNull();
  } finally {
    await context.close();
  }
});

test('bootstrap and invite entry points explain their one-time tokens', async ({browser}) => {
  const context = await browser.newContext({baseURL, viewport: {width: 1440, height: 1000}});
  const page = await context.newPage();
  try {
    await page.goto('/setup');
    await expect(page.getByRole('heading', {name: '建立首个管理员', exact: true})).toBeVisible();
    await expect(page.getByLabel('初始化令牌')).toBeVisible();
    await expect(page.getByText('令牌只保存在服务器 data/secrets/bootstrap-token 中', {exact: false})).toBeVisible();

    await page.goto('/invite?token=invite-token-example');
    await expect(page.getByRole('heading', {name: '接受邀请', exact: true})).toBeVisible();
    await expect(page.getByLabel('邀请令牌')).toHaveValue('invite-token-example');
  } finally {
    await context.close();
  }
});
