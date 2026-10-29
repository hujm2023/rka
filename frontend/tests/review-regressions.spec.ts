import {test,expect} from '@playwright/test';
import {catalogOf,getJson,signInIsolatedLearner,openDevice} from './support/e2e';
import {signIn} from './support/auth';

test('switching accounts in one browser does not expose the previous essay',async({page,browser})=>{
  const second=await openDevice(browser);
  let b;
  try{b=await signInIsolatedLearner(second.page,'isolation-b');}finally{await second.context.close();}
  await signInIsolatedLearner(page,'isolation-a');
  const catalog=await catalogOf(page),id=catalog.essays[0].id;
  await page.goto(`/essays?id=${id}`);await page.getByRole('tab',{name:'正文',exact:true}).click();
  const body=page.getByRole('textbox',{name:'正文',exact:true});
  await body.fill('只属于账号 A 的私有正文');
  await expect(page.locator('.page-heading .save-indicator')).toContainText('已保存到服务器');
  await page.getByRole('button',{name:'退出登录',exact:true}).click();
  await page.getByLabel('邮箱',{exact:true}).fill(b.email);await page.getByLabel('密码',{exact:true}).fill(b.password);
  await page.getByRole('button',{name:'登录',exact:true}).click();await expect(page.locator('.app-shell')).toBeVisible();
  await page.goto(`/essays?id=${id}`);await page.getByRole('tab',{name:'正文',exact:true}).click();
  await expect(body).toHaveValue('');
  const server=await getJson<any>(page,`/api/writing/essay/${id}`);expect(server.document.values).toEqual({});
});

test('a new device restores reading and scrolling preserves completion with integer offsets',async({page,browser})=>{
  const account=await signInIsolatedLearner(page,'reading-resume');
  const catalog=await catalogOf(page);const material=catalog.materials.find((m:any)=>m.title.includes('第6章'));
  const section=material.sections.filter((s:any)=>s.level===2||s.level===3).at(-2);
  const seeded=await page.request.post('/api/learning/reading',{data:{materialId:material.id,progress:.72,sectionId:section.id,offset:0,done:true}});
  expect(seeded.ok()).toBe(true);
  const second=await openDevice(browser);
  try{
    await signIn(second.page,account);
    const offsets:number[]=[],errors:number[]=[];
    second.page.on('response',response=>{if(response.url().endsWith('/api/learning/reading')){offsets.push(response.request().postDataJSON().offset);if(!response.ok())errors.push(response.status());}});
    await second.page.goto(`/knowledge?id=${material.id}`);
    await expect(second.page.getByRole('button',{name:'已标记学完',exact:true})).toBeVisible();
    await expect.poll(()=>second.page.evaluate(()=>scrollY)).toBeGreaterThan(50);
    let summary=await getJson<any>(second.page,'/api/learning/summary');
    expect(summary.readingProgress.find((r:any)=>r.materialId===material.id)).toMatchObject({done:true,progress:.72});
    await expect(second.page.getByText('阅读位置自动保存',{exact:true})).toBeVisible();
    await second.page.evaluate(()=>scrollTo(0,245.5));
    await expect.poll(()=>offsets.length).toBeGreaterThan(0);
    summary=await getJson<any>(second.page,'/api/learning/summary');
    expect(summary.readingProgress.find((r:any)=>r.materialId===material.id).done).toBe(true);
    expect(offsets.every(Number.isInteger)).toBe(true);expect(errors).toEqual([]);
  }finally{await second.context.close();}
});

test('the parent chapter from the review can start its descendant questions',async({page})=>{
  await signInIsolatedLearner(page,'parent-chapter');
  const response=await page.request.post('/api/practice/sessions',{data:{mode:'chapter',scope:{materialId:'material-7d28d105306c4b',sectionId:'section-13726'},count:5}});
  expect(response.status()).toBe(201);expect((await response.json()).session.questionIds).toHaveLength(5);
});
