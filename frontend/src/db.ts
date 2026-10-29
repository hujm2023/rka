import Dexie, { type EntityTable } from 'dexie';
import type {Attempt,Mark,Session,Draft,Reading,Chat} from './types';

type LearningDB = Dexie & {attempts:EntityTable<Attempt,'id'>; marks:EntityTable<Mark,'id'>; sessions:EntityTable<Session,'id'>; drafts:EntityTable<Draft,'id'>; reading:EntityTable<Reading,'id'>; chats:EntityTable<Chat,'id'>};
export function openLearningDB(name:string){
const instance=new Dexie(name) as LearningDB;
instance.version(1).stores({attempts:'id, questionId, createdAt',marks:'id',sessions:'id',drafts:'id,updatedAt',reading:'id,updatedAt',chats:'id,updatedAt'});
return instance;
}
export let db=openLearningDB('rka-learning-v1');
export function selectLearningAccount(id:string){db.close();db=openLearningDB(`rka-learning-user-${id}`);}
const tableNames = ['attempts','marks','sessions','drafts','reading','chats'] as const;
export function reportError(error:unknown) { window.dispatchEvent(new CustomEvent('rka-error',{detail:error instanceof Error?error.message:String(error)})); }
export async function saveSession(session:Session) { await db.sessions.put({...session,updatedAt:new Date().toISOString()}); }
export async function recordAnswer(session:Session,attempt:Attempt) {
  await db.transaction('rw',db.sessions,db.attempts,async()=>{await db.attempts.put(attempt);await saveSession(session);});
}
export async function exportBackup() {
  return db.transaction('r',db.tables,async()=>{
    const data:Record<string,unknown> = {};
    for(const name of tableNames) data[name] = await db.table(name).toArray();
    return {app:'rka',schemaVersion:1,exportedAt:new Date().toISOString(),data};
  });
}
const record = (x:unknown):x is Record<string,unknown> => !!x && typeof x==='object' && !Array.isArray(x);
const str = (x:unknown):x is string => typeof x==='string';
const date = (x:unknown) => str(x) && Number.isFinite(Date.parse(x));
const option = (x:unknown) => ['A','B','C','D'].includes(String(x));
const stringMap = (x:unknown) => record(x) && Object.values(x).every(str);
export function validateBackup(payload:unknown) {
  if(!record(payload)||payload.app!=='rka'||payload.schemaVersion!==1||!record(payload.data)) throw new Error('不是支持的 RKA v1 备份文件。');
  const data=payload.data;
  const validators:Record<string,(x:Record<string,unknown>)=>boolean> = {
    attempts:x=>str(x.questionId)&&str(x.revision)&&option(x.answer)&&typeof x.correct==='boolean'&&typeof x.assisted==='boolean'&&date(x.createdAt)&&typeof x.durationMs==='number'&&Number.isFinite(x.durationMs)&&x.durationMs>=0,
    marks:x=>typeof x.favorite==='boolean'&&typeof x.uncertain==='boolean'&&str(x.note),
    sessions:x=>Array.isArray(x.questionIds)&&x.questionIds.every(str)&&typeof x.index==='number'&&Number.isInteger(x.index)&&x.index>=0&&x.index<Math.max(1,x.questionIds.length)&&record(x.answers)&&Object.values(x.answers).every(a=>record(a)&&(a.selected===undefined||option(a.selected))&&(a.submitted===undefined||typeof a.submitted==='boolean')&&(a.assisted===undefined||typeof a.assisted==='boolean'))&&record(x.filters)&&['chapter','all','exam','wrong','favorites','review'].includes(String(x.filters.mode))&&['module','term','source','query'].every(k=>str((x.filters as Record<string,unknown>)[k]))&&typeof x.filters.count==='number'&&Number.isFinite(x.filters.count)&&x.filters.count>0&&typeof x.filters.includeMissing==='boolean'&&(x.filters.materialId===undefined||str(x.filters.materialId))&&(x.filters.sectionId===undefined||str(x.filters.sectionId))&&typeof x.elapsed==='number'&&Number.isFinite(x.elapsed)&&x.elapsed>=0&&date(x.updatedAt)&&str(x.bankVersion),
    drafts:x=>['case','essay'].includes(String(x.kind))&&stringMap(x.values)&&date(x.updatedAt),
    reading:x=>typeof x.progress==='number'&&Number.isFinite(x.progress)&&x.progress>=0&&x.progress<=1&&typeof x.done==='boolean'&&date(x.updatedAt)&&(x.sectionId===undefined||str(x.sectionId))&&(x.sectionOffset===undefined||(typeof x.sectionOffset==='number'&&Number.isFinite(x.sectionOffset))),
    chats:x=>date(x.updatedAt)&&Array.isArray(x.messages)&&x.messages.every(m=>record(m)&&str(m.id)&&['user','assistant'].includes(String(m.role))&&str(m.content)&&date(m.createdAt)&&(m.sources===undefined||(Array.isArray(m.sources)&&m.sources.every(s=>record(s)&&str(s.id)&&str(s.title)&&str(s.text)))))
  };
  for(const name of tableNames) {
    const rows=data[name];
    if(!Array.isArray(rows)||rows.length>100000||!rows.every(x=>record(x)&&str(x.id)&&x.id.length>0&&validators[name](x))) throw new Error(`备份中的 ${name} 数据不完整，未修改现有记录。`);
    if(new Set(rows.map(x=>x.id)).size!==rows.length) throw new Error(`备份中的 ${name} 存在重复 ID。`);
  }
  return data as Record<typeof tableNames[number],Record<string,unknown>[]>;
}
export async function importBackup(payload:unknown) {
  const data=validateBackup(payload);
  await db.transaction('rw',db.tables,async()=>{for(const name of tableNames){await db.table(name).clear();await db.table(name).bulkPut(data[name]);}});
}
export function convertLegacy(payload:unknown, validIds:Set<string>) {
  if(!record(payload)||![1,2].includes(Number(payload.schemaVersion))||!Array.isArray(payload.attempts)) throw new Error('不支持的旧版进度文件。');
  const attempts:Attempt[]=[]; const marks:Mark[]=[];
  for(const raw of payload.attempts) {
    if(!record(raw)||!str(raw.questionId)||!validIds.has(raw.questionId)) continue;
    if(!option(raw.answer)||typeof raw.correct!=='boolean'||!date(raw.answeredAt)) throw new Error('旧版作答记录格式错误。');
    attempts.push({id:`legacy-${raw.questionId}-${raw.answeredAt}-${raw.id ?? ''}`,questionId:raw.questionId,revision:'legacy',answer:raw.answer as Attempt['answer'],correct:raw.correct,assisted:false,createdAt:raw.answeredAt as string,durationMs:typeof raw.durationMs==='number'?Math.max(0,raw.durationMs):0});
  }
  if(payload.bookmarks!==undefined&&!Array.isArray(payload.bookmarks)) throw new Error('旧版收藏格式错误。');
  for(const raw of (payload.bookmarks||[]) as unknown[]) if(record(raw)&&str(raw.questionId)&&validIds.has(raw.questionId)) marks.push({id:raw.questionId,favorite:true,uncertain:false,note:''});
  return {attempts,marks};
}
export async function mergeLegacy(data:ReturnType<typeof convertLegacy>) {await db.transaction('rw',db.attempts,db.marks,async()=>{await db.attempts.bulkPut(data.attempts);for(const mark of data.marks){const old=await db.marks.get(mark.id);await db.marks.put({...mark,...old,favorite:true});}});}
