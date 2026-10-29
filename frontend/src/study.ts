import type {Attempt,Question,Mark,Filters,KnowledgeLinks} from './types';
export function latestAttempts(attempts:Attempt[]) {const map=new Map<string,Attempt>();for(const a of attempts) if(!map.has(a.questionId)||map.get(a.questionId)!.createdAt<=a.createdAt) map.set(a.questionId,a);return map;}
export function isDue(attempt:Attempt,now=Date.now()) {const days=!attempt.correct?1:attempt.assisted?2:7;return now-Date.parse(attempt.createdAt)>=days*86400000;}
export function belongsToMaterial(links:KnowledgeLinks,questionId:string,materialId:string,sectionId?:string){return !!links[questionId]?.some(link=>link.materialId===materialId&&(!sectionId||link.sectionId===sectionId||link.sectionPath.includes(sectionId)));}
export function buildQueue(questions:Question[],attempts:Attempt[],marks:Mark[],filters:Filters,knowledgeLinks:KnowledgeLinks={}) {
  const latest=latestAttempts(attempts),favorites=new Set(marks.filter(m=>m.favorite).map(m=>m.id));
  return questions.filter(q=>{
    const a=latest.get(q.id);
    if(!filters.includeMissing&&q.figureMissing) return false;
    if(filters.materialId&&!belongsToMaterial(knowledgeLinks,q.id,filters.materialId,filters.sectionId))return false;
    if(filters.module!=='all'&&q.module!==filters.module) return false;
    if(filters.term!=='all'&&q.term!==filters.term) return false;
    if(filters.source!=='all'&&q.sourceType!==filters.source) return false;
    if(filters.query&&!`${q.stem} ${q.knowledge} ${q.term}`.toLowerCase().includes(filters.query.toLowerCase()))return false;
    if(filters.mode==='wrong')return a?.correct===false;
    if(filters.mode==='favorites')return favorites.has(q.id);
    if(filters.mode==='review')return !!a&&isDue(a);
    return true;
  }).sort((a,b)=>{
    if(filters.mode==='exam')return a.questionNo-b.questionNo;
    if(filters.mode==='all'||filters.mode==='chapter') {const visited=Number(latest.has(a.id))-Number(latest.has(b.id));if(visited)return visited;}
    return b.term.localeCompare(a.term,'zh')||a.questionNo-b.questionNo;
  });
}
export function fmtTime(seconds:number) {return `${Math.floor(seconds/60).toString().padStart(2,'0')}:${Math.floor(seconds%60).toString().padStart(2,'0')}`;}
export function percent(n:number,total:number) {return total?Math.round(n/total*100):0;}
export function localDay(date=new Date()) {return `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}`;}
