import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { linkKnowledge } from './link-knowledge.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const out = path.join(root, 'rka/content');
await fs.mkdir(path.join(out, 'lessons'), { recursive: true });
await fs.mkdir(path.join(out, 'materials'), { recursive: true });
const read = (name) => fs.readFile(path.join(root, name), 'utf8');
const hash = (s) => crypto.createHash('sha256').update(s).digest('hex').slice(0, 14);
const clean = (s = '') => String(s).replace(/\r\n/g, '\n').trim();
const sourceUrl = (repo, file) => `https://github.com/${repo}/blob/main/${file.split('/').map(encodeURIComponent).join('/')}`;
const bank = JSON.parse(await read('ruankao-architect-practice/data/bank.json'));
const figures = JSON.parse(await read('ruankao-architect-practice/data/figures.json')).figures;
const questionBlocks = (text) => [...text.matchAll(/^#{2,3}\s+第(\d+)题[^\n]*\n([\s\S]*?)(?=^#{1,3}\s|$(?![\s\S]))/gm)];
const inferModule = (s) => {
  for (const [key, pattern] of Object.entries({database:/数据库|SQL|范式|事务|关系代数/, architecture:/架构|微服务|ATAM|SOA|质量属性|设计模式|DDD/, security:/加密|安全|签名|SSL|权限|访问控制/, network:/网络|TCP|IP地址|路由|子网|HTTP/, computer_foundation:/操作系统|进程|CPU|内存|流水线|信号量|Cache|调度/, new_technology:/人工智能|Transformer|机器学习|向量|区块链/, software_engineering:/软件工程|需求|测试|UML|开发|维护/, project_management:/项目|成本|进度|风险/})) if (pattern.test(s)) return key;
  return 'other';
};
const choices = bank.choices.map(q => ({...q, sourceUrl: sourceUrl('AndyNull/ruankao-architect-practice', 'data/bank.json'), sourceLabel: '练题台整理 · 第三方参考答案', figure: figures[q.id] || null}));
const added = [];
const failures = [];
const recallFile = '真题/系统架构设计师/2026年上半年/综合知识.md';
function parseOptions(body) {
  const options = {};
  for (const m of body.matchAll(/^\s*(?:-\s*)?([A-D])[.．、]\s*(.+)$/gm)) options[m[1]] = m[2].trim();
  return options;
}
function addQuestion({id, body, answerBody, number, term, sourceType, file, paper}) {
  const options = parseOptions(body);
  const answer = answerBody.match(/\*\*(?:验证)?答案[：:]?\*\*[：:]?\s*([A-D])/u)?.[1] || answerBody.match(/\*\*验证答案[：:]\s*([A-D])\*\*/u)?.[1];
  if (!answer || Object.keys(options).length !== 4) { failures.push({id, reason:'缺少四个选项或参考答案'}); return; }
  const stem = clean(body.split(/^\s*(?:\*\*选项\*\*|(?:-\s*)?A[.．、])/m)[0]).replace(/\n---\s*$/, '');
  const analysis = clean(answerBody.split(/\*\*(?:验证依据|解析)[：:]?\*\*[：:]?/u)[1] || '').replace(/\n---\s*$/, '');
  if (!stem) {failures.push({id, reason:'空题干'});return;}
  const q = {id, stem, options, answer, analysis, term, sourceType, paper, questionNo:number, module:inferModule(stem), knowledge:'', difficulty:'', sourceFile:file, sourceLabel:sourceType === 'real' ? '2026 考生回忆版 · 第三方参考答案' : '原创模拟题 · 第三方参考答案', sourceUrl:sourceUrl('wujiaming88/awesome-ruankao', file), figure:null};
  choices.push(q); added.push(id);
}
for (const m of questionBlocks(await read(`awesome-ruankao/${recallFile}`))) addQuestion({id:`real-2026年上半年-${m[1].padStart(3,'0')}`,body:m[2],answerBody:m[2],number:Number(m[1]),term:'2026年上半年',sourceType:'real',file:recallFile,paper:'回忆版'});
for (let n = 1; n <= 5; n++) {
  const dir = `模拟题/系统架构设计师/2026年10月/模拟卷${n}`;
  const answers = new Map(questionBlocks(await read(`awesome-ruankao/${dir}/综合知识_答案.md`)).map(m => [m[1],m[2]]));
  for (const m of questionBlocks(await read(`awesome-ruankao/${dir}/综合知识_题目.md`))) addQuestion({id:`mock-2026年10月-模拟卷${n}-${m[1].padStart(3,'0')}`,body:m[2],answerBody:answers.get(m[1]) || '',number:Number(m[1]),term:`2026年10月 模拟卷${n}`,sourceType:'mock',file:`${dir}/综合知识_题目.md`,paper:`模拟卷${n}`});
}
const ids = new Set();
for (const q of choices) {
  if (ids.has(q.id)) throw new Error(`Duplicate ID: ${q.id}`);
  ids.add(q.id);
  q.revision = hash(JSON.stringify([q.stem,q.options,q.answer]));
  q.figureMissing = /如[下]?图|下图|图中|图所示/.test(q.stem) && !q.figure && !/```mermaid|!\[/.test(q.stem);
  q.multiBlank = [...q.stem.matchAll(/[（(]\s*(\d+)\s*[）)]/g)].some(m => Number(m[1]) === q.questionNo);
}
const lessons = [];
const searchIndex = {};
const materials = [];
const materialSearch = {};
async function addLessons(file, book, repo, license, originalUrl) {
  const sourceText = await read(file);
  const text = clean(sourceText);
  const kind = file.includes('/textbook/') ? 'textbook' : file.includes('/outline/') ? 'outline' : file.startsWith('ruankao-architect-notes/') ? 'notes' : 'guide';
  const materialId = `material-${hash(file)}`;
  const relative = file.substring(file.indexOf('/') + 1);
  const sections = [];
  let fence = '';
  for (const match of sourceText.matchAll(/^.*(?:\n|$)/gm)) {
    const line = match[0].replace(/\r?\n$/, '');
    const delimiter = line.match(/^\s*(`{3,}|~{3,})/);
    if (delimiter) { if (!fence) fence = delimiter[1][0]; else if (delimiter[1][0] === fence) fence = ''; continue; }
    if (fence) continue;
    const heading = line.match(/^(#{1,4})\s+(.+?)\s*#*$/);
    if (heading) sections.push({id:`section-${match.index}`,title:heading[2].replace(/[*`]/g,''),level:heading[1].length,start:match.index});
  }
  const title = ['textbook','outline'].includes(kind) ? book.split(' · ').slice(1).join(' · ') : book;
  materials.push({id:materialId,title,book,kind,module:inferModule(title),sourceFile:file,sourceUrl:originalUrl || sourceUrl(repo,relative),license,chars:sourceText.length,minutes:Math.max(1,Math.ceil(sourceText.length/500)),sections,sourceHash:crypto.createHash('sha256').update(sourceText).digest('hex'),excerpt:sections.filter(s=>s.level===2).slice(0,4).map(s=>s.title).join(' · ')});
  materialSearch[materialId] = sourceText.replace(/[#*|>`]/g,'').replace(/\s+/g,' ');
  await fs.copyFile(path.join(root,file),path.join(out,'materials',`${materialId}.md`));
  const blocks = text.split(/(?=^##\s+)/m);
  const occurrences = new Map();
  for (const raw of blocks) {
    if (raw.trim().length < 120) continue;
    const title = clean(raw.match(/^#{1,2}\s+(.+)/m)?.[1] || book).replace(/[📚📕📘📗📙⭐🔥🚀]/gu,'').trim();
    const count = occurrences.get(title) || 0; occurrences.set(title,count+1);
    const id = hash(`${file}:${title}:${count}`);
    const body = raw.trim();
    searchIndex[id] = body.replace(/[#*|>`]/g,'').replace(/\s+/g,' ');
    const relative = file.substring(file.indexOf('/')+1);
    const lesson = {id,title,book,module:inferModule(`${book} ${title}`),sourceFile:file,sourceUrl:originalUrl || sourceUrl(repo,relative),license,chars:body.length,minutes:Math.max(1,Math.ceil(body.length/500)),excerpt:body.replace(/[#*|>`]/g,'').replace(/\s+/g,' ').slice(0,170)};
    lessons.push(lesson); await fs.writeFile(path.join(out,'lessons',`${id}.md`),body);
  }
}
for (const file of (await fs.readdir(path.join(root,'ruankao-architect-notes'))).sort()) {
  if (file.endsWith('.md') && file !== 'README.md') await addLessons(`ruankao-architect-notes/${file}`,file.replace('.md',''),'pianpian315/ruankao-architect-notes','CC BY-NC-SA 4.0');
}
for (const file of ['docs/备考攻略/案例分析答题技巧.md','案例分析/答题模板/系统架构设计师答题模板.md','论文/高频主题汇总.md']) {
  await addLessons(`awesome-ruankao/${file}`,path.basename(file,'.md'),'wujiaming88/awesome-ruankao','CC BY-SA 4.0');
}
const studyMaterials=JSON.parse(await read('ruankao-architect-practice/data/study-materials.json')).materials;
for(const material of studyMaterials) {
  const file=`ruankao-architect-practice/${decodeURIComponent(material.localUrl).replace(/^\.\//,'')}`;
  await addLessons(file,`${material.groupLabel} · ${material.title}`,'YoungHong1992/ruankao-senior-architecture-designer','第三方学习资料 · 见原始来源声明',material.sourceUrl);
}
const subjective = (q) => ({...q,revision:hash(JSON.stringify(q)),sourceUrl:sourceUrl('AndyNull/ruankao-architect-practice','data/bank.json')});
const kindOrder = {textbook:0,outline:1,notes:2,guide:3};
materials.sort((a,b)=>kindOrder[a.kind]-kindOrder[b.kind] || (Number(a.title.match(/第(\d+)章/)?.[1]||0)-Number(b.title.match(/第(\d+)章/)?.[1]||0)));
const output = {version:hash(JSON.stringify([choices,lessons])),choices,cases:bank.cases.map(subjective),essays:bank.essays.map(subjective),lessons,materials};
output.knowledgeLinks=await linkKnowledge(choices,materials,out);
await fs.writeFile(path.join(out,'knowledge-links-audit.json'),JSON.stringify({method:'lexical-v1',note:'按题干、已有知识标签与原文匹配；非官方考点映射，存在漏配或误配的可能。',linkedQuestions:Object.keys(output.knowledgeLinks).length,unlinkedQuestions:choices.filter(q=>!output.knowledgeLinks[q.id]).map(q=>q.id),byMaterial:materials.map(m=>({id:m.id,title:m.title,count:choices.filter(q=>!q.figureMissing&&output.knowledgeLinks[q.id]?.some(link=>link.materialId===m.id)).length}))},null,2));
await fs.writeFile(path.join(out,'catalog.json'),JSON.stringify(output));
await fs.writeFile(path.join(out,'search.json'),JSON.stringify(searchIndex));
await fs.writeFile(path.join(out,'material-search.json'),JSON.stringify(materialSearch));
const report = {version:output.version,counts:{choices:choices.length,real:choices.filter(q=>q.sourceType==='real').length,mock:choices.filter(q=>q.sourceType==='mock').length,cases:bank.cases.length,essays:bank.essays.length,lessons:lessons.length,materials:materials.length},addedChoices:added.length,skipped:failures,figureReview:choices.filter(q=>q.figureMissing).map(q=>q.id),notes:['题量为题目记录数；共享题干的不同小题分别保留。','缺图题默认不进入练习队列，可在题库筛选中查看。','所有答案保留来源属性，不代表官方答案。','仅复用内容数据；未复制原项目实现。','materials 保留完整源文件，sourceHash 可核对完整性；lessons 仅用于检索和兼容旧链接。']};
await fs.writeFile(path.join(out,'audit.json'),JSON.stringify(report,null,2));
await fs.mkdir(path.join(out,'licenses'),{recursive:true});
for (const project of ['ruankao-architect-practice','ruankao-architect-notes','awesome-ruankao']) await fs.copyFile(path.join(root,project,'LICENSE'),path.join(out,'licenses',`${project}.txt`));
await fs.copyFile(path.join(root,'ruankao-architect-practice/NOTICE.md'),path.join(out,'licenses/practice-NOTICE.md'));
console.log(JSON.stringify({counts:report.counts,added:added.length,skipped:failures.length,figureReview:report.figureReview.length},null,2));
