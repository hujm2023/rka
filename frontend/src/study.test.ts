import {expect,it} from 'vitest';
import {buildQueue,isDue,belongsToMaterial} from './study';
import {defaultFilters,type Question,type Attempt} from './types';
const question=(id:string,no:number,extra={})=>({id,questionNo:no,term:'2026年上半年',sourceType:'real',module:'architecture',stem:id,knowledge:'',figureMissing:false,...extra}) as Question;
const attempt=(id:string,correct:boolean,assisted=false)=>({id,questionId:id,correct,assisted,createdAt:'2026-10-20T00:00:00Z'}) as Attempt;
it('keeps multi-blank questions distinct and excludes missing diagrams by default',()=>{const questions=[question('q1',1),question('q2',2),question('q3',3,{figureMissing:true})];expect(buildQueue(questions,[],[],defaultFilters).map(q=>q.id)).toEqual(['q1','q2']);});
it('wrong mode uses latest attempt and favorites are independent',()=>{const qs=[question('q1',1),question('q2',2)];const attempts=[attempt('q1',false),{...attempt('q1',true),createdAt:'2026-10-21T00:00:00Z'},attempt('q2',false)];expect(buildQueue(qs,attempts,[],{...defaultFilters,mode:'wrong'}).map(q=>q.id)).toEqual(['q2']);expect(buildQueue(qs,attempts,[{id:'q1',favorite:true,note:'',uncertain:false}],{...defaultFilters,mode:'favorites'}).map(q=>q.id)).toEqual(['q1']);});
it('assisted success becomes due before independent success',()=>{const now=Date.parse('2026-10-23T00:00:00Z');expect(isDue(attempt('q1',true,true),now)).toBe(true);expect(isDue(attempt('q1',true,false),now)).toBe(false);});
it('orders a paper by original question number',()=>{expect(buildQueue([question('q9',9),question('q2',2)],[],[],{...defaultFilters,mode:'exam'}).map(q=>q.id)).toEqual(['q2','q9']);});
it('chapter and subsection exercises use exact associations, not broad subject labels',()=>{
  const links={q1:[{materialId:'database',sectionId:'third-normal',sectionTitle:'第三范式',sectionPath:['normalization'],terms:['范式'],score:50}],q2:[{materialId:'database',sectionId:'transactions',sectionTitle:'事务',sectionPath:[],terms:['事务'],score:40}]};
  const qs=[question('q1',1),question('q2',2),question('q3',3)];
  expect(buildQueue(qs,[],[],{...defaultFilters,materialId:'database'},links).map(q=>q.id)).toEqual(['q1','q2']);
  expect(buildQueue(qs,[],[],{...defaultFilters,materialId:'database',sectionId:'normalization'},links).map(q=>q.id)).toEqual(['q1']);
  expect(belongsToMaterial(links,'q3','database')).toBe(false);
  expect(buildQueue(qs,[],[],{...defaultFilters,materialId:'unknown'},links)).toEqual([]);
});
