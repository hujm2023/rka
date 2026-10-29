import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {expect,it} from 'vitest';
import type {Catalog} from './types';

const catalog=JSON.parse(readFileSync(new URL('../../content/catalog.json',import.meta.url),'utf8')) as Catalog;
it('ships complete source documents matching their original fingerprints',()=>{
  expect(catalog.materials).toHaveLength(38);
  expect(catalog.materials.filter(m=>m.kind==='textbook')).toHaveLength(21);
  for(const material of catalog.materials){
    const body=readFileSync(new URL(`../../content/materials/${material.id}.md`,import.meta.url));
    expect(createHash('sha256').update(body).digest('hex')).toBe(material.sourceHash);
    expect(body.toString().length).toBe(material.chars);
    for(const section of material.sections)expect(body.toString().slice(section.start)).toMatch(/^#{1,4} /);
  }
});
it('keeps old lesson links resolvable to complete parent documents',()=>{
  for(const lesson of catalog.lessons)expect(catalog.materials.some(m=>m.sourceFile===lesson.sourceFile)).toBe(true);
});
it('every question association points to real source headings, including ancestors',()=>{
  const questionIds=new Set(catalog.choices.map(q=>q.id));
  for(const [questionId,links] of Object.entries(catalog.knowledgeLinks||{})){
    expect(questionIds.has(questionId)).toBe(true);
    for(const link of links){
      const material=catalog.materials.find(m=>m.id===link.materialId);
      expect(material).toBeDefined();
      expect(material?.sections.find(s=>s.id===link.sectionId)?.title).toBe(link.sectionTitle);
      for(const ancestor of link.sectionPath)expect(material?.sections.some(s=>s.id===ancestor)).toBe(true);
    }
  }
});
it('anchors representative concepts correctly and does not assign unsupported new concepts',()=>{
  const titleFor=(id:string)=>(catalog.knowledgeLinks?.[id]||[]).map(l=>`${catalog.materials.find(m=>m.id===l.materialId)?.title} ${l.sectionTitle}`).join(' ');
  expect(titleFor('real-2026年上半年-001')).toContain('访问控制矩阵');
  expect(titleFor('mock-2026年5月-模拟卷1-027')).toContain('ATAM');
  expect(titleFor('mock-2026年5月-模拟卷3-054')).toMatch(/范式|NF/);
  const quantum=catalog.choices.find(q=>q.stem.includes('量子比特'));
  expect(quantum).toBeDefined();expect(catalog.knowledgeLinks?.[quantum!.id]).toBeUndefined();
});
