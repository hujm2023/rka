import {accountRequest,suspendAccount} from './account';

// Thin HTTP client for the RKA platform API. Every browser call uses the
// same-origin HttpOnly session cookie; tokens never exist in JavaScript.
import type {Catalog, Case, Essay, Material, Question, ServerSession, Mark, SourceType} from './types';

export type ApiErrorBody = {code:string;message:string;details?:unknown};

export class ApiError extends Error {
  status:number; code:string; details:unknown;
  constructor(status:number, body:ApiErrorBody) {
    super(body.message || `请求失败（HTTP ${status}）`);
    this.name = 'ApiError';
    this.status = status;
    this.code = body.code || 'error';
    this.details = body.details;
  }
  get isConflict(){return this.status===409;}
  get isUnauthorized(){return this.status===401;}
}

export function errorText(error:unknown):string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

type QueryValue = string|number|boolean|null|undefined;
export type RequestOptions = {
  method?:string; json?:unknown; body?:BodyInit; headers?:Record<string,string>;
  query?:Record<string,QueryValue>; signal?:AbortSignal;
};

function buildPath(path:string, query?:Record<string,QueryValue>){
  if(!query) return path;
  const params=new URLSearchParams();
  for(const [key,value] of Object.entries(query)) if(value!==undefined&&value!==null&&value!=='') params.set(key,String(value));
  const suffix=params.toString();
  return suffix?`${path}${path.includes('?')?'&':'?'}${suffix}`:path;
}

async function readBody(response:Response):Promise<unknown>{
  const text=await response.text();
  if(!text) return undefined;
  try{return JSON.parse(text);}catch{return text;}
}

function envelope(body:unknown,status:number):ApiErrorBody{
  const record=body&&typeof body==='object'?body as Record<string,unknown>:{};
  const nested=record.error;
  if(nested&&typeof nested==='object'){
    const err=nested as Record<string,unknown>;
    return {code:String(err.code||'error'),message:String(err.message||`请求失败（HTTP ${status}）`),details:err.details};
  }
  if(typeof record.message==='string') return {code:String(record.code||'error'),message:record.message,details:record.details};
  if(typeof body==='string'&&body.trim()) return {code:'error',message:body.trim()};
  return {code:'error',message:`请求失败（HTTP ${status}）`};
}

export async function api<T=unknown>(path:string, options:RequestOptions={}):Promise<T>{
  const account=path.startsWith('/api/auth/')?{headers:{},signal:options.signal}:accountRequest(options.signal);
  const headers:Record<string,string>={Accept:'application/json',...account.headers,...options.headers};
  let body=options.body;
  if(options.json!==undefined){
    headers['Content-Type']='application/json';
    body=JSON.stringify(options.json);
  }
  let response:Response;
  try{
    response=await fetch(buildPath(path,options.query),{
      method:options.method||(body?'POST':'GET'),
      credentials:'same-origin',
      headers,
      body,
      signal:account.signal,
    });
  }catch(error){
    if((error as Error)?.name==='AbortError') throw error;
    throw new ApiError(0,{code:'network_error',message:'网络连接不可用，已保留本地更改。'});
  }
  const payload=await readBody(response);
  if(!response.ok){
    const parsed=envelope(payload,response.status);
    const error=new ApiError(response.status,parsed);
    if(parsed.code==='account_changed'){suspendAccount();window.location.reload();}
    if(response.status===401&&!path.startsWith('/api/auth/login')){
      window.dispatchEvent(new CustomEvent('rka-unauthorized'));
    }
    throw error;
  }
  return payload as T;
}

export const apiGet=<T=unknown>(path:string,query?:Record<string,QueryValue>,signal?:AbortSignal)=>api<T>(path,{query,signal});
export const apiPost=<T=unknown>(path:string,json?:unknown)=>api<T>(path,{method:'POST',json});
export const apiPut=<T=unknown>(path:string,json?:unknown)=>api<T>(path,{method:'PUT',json});
export const apiPatch=<T=unknown>(path:string,json?:unknown)=>api<T>(path,{method:'PATCH',json});
export const apiDelete=<T=unknown>(path:string)=>api<T>(path,{method:'DELETE'});

export async function apiText(path:string):Promise<string>{
  const account=accountRequest();
  const response=await fetch(path,{credentials:'same-origin',signal:account.signal,headers:{...account.headers,Accept:'text/markdown,text/plain;q=0.9,*/*;q=0.8'}});
  const text=await response.text();
  if(!response.ok){
    let payload:unknown=text;
    try{payload=JSON.parse(text);}catch{/* plain text error */}
    throw new ApiError(response.status,envelope(payload,response.status));
  }
  return text;
}

// ------------------------------------------------------------------ server clock

let serverOffset=0;
export async function syncServerClock(){
  try{
    const state=await apiGet<{serverTime?:string}>('/api/sync/state');
    const parsed=state?.serverTime?Date.parse(state.serverTime):NaN;
    if(Number.isFinite(parsed)) serverOffset=parsed-Date.now();
  }catch{/* the clock endpoint may not be available yet; local time is a fallback */}
  return serverOffset;
}
export const serverNow=()=>Date.now()+serverOffset;

// ------------------------------------------------------------------ SSE

export type SseEvent={event:string;id?:string;data:unknown};

export async function streamSse(path:string,onEvent:(event:SseEvent)=>void,signal:AbortSignal,lastEventId?:string){
  const account=accountRequest(signal);
  const headers:Record<string,string>={Accept:'text/event-stream',...account.headers};
  if(lastEventId) headers['Last-Event-ID']=lastEventId;
  let response:Response;
  try{
    response=await fetch(path,{credentials:'same-origin',headers,signal:account.signal});
  }catch(error){
    if((error as Error)?.name==='AbortError') throw error;
    throw new ApiError(0,{code:'network_error',message:'事件连接已断开。'});
  }
  if(!response.ok){
    const payload=await readBody(response);
    const error=new ApiError(response.status,envelope(payload,response.status));
    if(response.status===401) window.dispatchEvent(new CustomEvent('rka-unauthorized'));
    throw error;
  }
  if(!response.body) throw new ApiError(0,{code:'stream_empty',message:'事件流没有内容。'});
  const reader=response.body.getReader(),decoder=new TextDecoder();
  let buffer='';
  const dispatch=(block:string)=>{
    let event='message',id:string|undefined,dataLines:string[]=[];
    for(const rawLine of block.split('\n')){
      const line=rawLine.replace(/\r$/,'');
      if(!line||line.startsWith(':')) continue;
      const colon=line.indexOf(':');
      const field=colon<0?line:line.slice(0,colon);
      let value=colon<0?'':line.slice(colon+1);
      if(value.startsWith(' ')) value=value.slice(1);
      if(field==='event') event=value;
      else if(field==='id') id=value;
      else if(field==='data') dataLines.push(value);
    }
    if(!dataLines.length) return;
    const raw=dataLines.join('\n');
    let data:unknown=raw;
    try{data=JSON.parse(raw);}catch{/* keep raw text */}
    onEvent({event,id,data});
  };
  try{
    while(true){
      const item=await reader.read();
      buffer+=decoder.decode(item.value,{stream:!item.done});
      let boundary:number;
      while((boundary=buffer.indexOf('\n\n'))>=0){
        dispatch(buffer.slice(0,boundary));
        buffer=buffer.slice(boundary+2);
      }
      if(item.done){if(buffer.trim()) dispatch(buffer);break;}
    }
  }finally{
    await reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

// ------------------------------------------------------------------ normalizers
// The Go JSON contract is camelCase. Normalizers keep the UI tolerant of small
// differences between packages while never inventing missing data.

const record=(value:unknown):value is Record<string,unknown>=>!!value&&typeof value==='object'&&!Array.isArray(value);
const str=(value:unknown,fallback='')=>typeof value==='string'?value:fallback;
const num=(value:unknown,fallback=0)=>typeof value==='number'&&Number.isFinite(value)?value:fallback;

// normalizeSourceType keeps the server's three origins apart. Unknown values
// fall back to "real" so an older server cannot make a mock paper look real.
function normalizeSourceType(value:unknown):SourceType{
  const text=str(value).toLowerCase();
  if(text==='ai') return 'ai';
  if(text==='mock') return 'mock';
  return 'real';
}

function normalizeQuestion(raw:unknown,index:number):Question{
  const q=record(raw)?raw:{};
  let options=q.options;
  if(Array.isArray(options)){
    const map:Record<string,string>={};
    for(const entry of options){
      if(record(entry)){const key=str(entry.key||entry.id||entry.option).toUpperCase();const text=str(entry.text||entry.label||entry.value);if(key)map[key]=text;}
    }
    options=map;
  }
  const optionRecord=record(options)?options:{};
  return {
    id:str(q.id,`question-${index}`),revision:str(q.revision,''),
    stem:str(q.stem),options:{A:str(optionRecord.A),B:str(optionRecord.B),C:str(optionRecord.C),D:str(optionRecord.D)},
    answer:(['A','B','C','D'].includes(str(q.answer))?str(q.answer):undefined) as Question['answer'],
    analysis:typeof q.analysis==='string'?q.analysis:undefined,
    term:str(q.term),paper:typeof q.paper==='string'?q.paper:undefined,
    questionNo:num(q.questionNo,index+1),module:str(q.module,'other'),knowledge:str(q.knowledge),
    difficulty:typeof q.difficulty==='string'?q.difficulty:undefined,
    sourceType:normalizeSourceType(q.sourceType),
    sourceFile:typeof q.sourceFile==='string'?q.sourceFile:undefined,
    sourceLabel:typeof q.sourceLabel==='string'?q.sourceLabel:undefined,
    sourceUrl:str(q.sourceUrl),
    figureMissing:!!q.figureMissing,multiBlank:!!q.multiBlank,
    figure:record(q.figure)?q.figure as Question['figure']:null,
    reviewStatus:typeof q.reviewStatus==='string'?q.reviewStatus:undefined,
  };
}

export function normalizeMaterial(raw:unknown,index:number):Material{
  const m=record(raw)?raw:{};
  const sectionsRaw=Array.isArray(m.sections)?m.sections:[];
  const sections=sectionsRaw.map((entry,sectionIndex)=>{
    const s=record(entry)?entry:{};
    const start=num(s.start,0);
    return {id:str(s.id,`section-${start}-${sectionIndex}`),title:str(s.title),level:num(s.level,2),start,key:typeof s.key==='string'?s.key:undefined,path:Array.isArray(s.path)?s.path.filter((p):p is string=>typeof p==='string'):undefined};
  });
  return {
    id:str(m.id,`material-${index}`),title:str(m.title),book:str(m.book),
    kind:(['textbook','outline','notes','guide'].includes(str(m.kind))?str(m.kind):'textbook') as Material['kind'],
    module:str(m.module,'other'),sourceFile:str(m.sourceFile),sourceUrl:str(m.sourceUrl),license:str(m.license),
    chars:num(m.chars),minutes:num(m.minutes),sections,excerpt:str(m.excerpt),
    sourceHash:typeof m.sourceHash==='string'?m.sourceHash:undefined,
    version:typeof m.version==='number'?m.version:undefined,
    versionId:typeof m.versionId==='string'?m.versionId:(typeof m.currentVersionId==='string'?m.currentVersionId:undefined),
    currentVersionId:typeof m.currentVersionId==='string'?m.currentVersionId:undefined,
    indexStatus:typeof m.indexStatus==='string'?m.indexStatus:undefined,
    status:typeof m.status==='string'?m.status:undefined,
  };
}

function normalizeCase(raw:unknown,index:number):Case{
  const c=record(raw)?raw:{};
  const subs=Array.isArray(c.subQuestions)?c.subQuestions:(Array.isArray(c.sub_questions)?c.sub_questions:[]);
  return {
    id:str(c.id,`case-${index}`),revision:str(c.revision),title:str(c.title),description:str(c.description),
    module:str(c.module,'other'),term:str(c.term),sourceType:normalizeSourceType(c.sourceType),sourceUrl:str(c.sourceUrl),
    subQuestions:subs.map((entry)=>{
      const s=record(entry)?entry:{};
      return {prompt:str(s.prompt),referenceAnswer:typeof s.referenceAnswer==='string'?s.referenceAnswer:(typeof s.reference_answer==='string'?s.reference_answer:undefined)};
    }),
  };
}

function normalizeEssay(raw:unknown,index:number):Essay{
  const e=record(raw)?raw:{};
  return {id:str(e.id,`essay-${index}`),revision:str(e.revision),title:str(e.title),prompt:str(e.prompt),writingPoints:str(e.writingPoints),module:str(e.module,'other'),term:str(e.term),sourceType:normalizeSourceType(e.sourceType),sourceUrl:str(e.sourceUrl)};
}

export function normalizeCatalog(raw:unknown):Catalog{
  const c=record(raw)?raw:{};
  const questionsRaw=Array.isArray(c.questions)?c.questions:(Array.isArray(c.choices)?c.choices:[]);
  const links=record(c.knowledgeLinks)?c.knowledgeLinks:{};
  const knowledgeLinks:Record<string,import('./types').KnowledgeLink[]>={};
  for(const [questionId,list] of Object.entries(links)){
    if(!Array.isArray(list)) continue;
    knowledgeLinks[questionId]=list.map((entry)=>{
      const l=record(entry)?entry:{};
      return {materialId:str(l.materialId),sectionId:str(l.sectionId),sectionKey:typeof l.sectionKey==='string'?l.sectionKey:undefined,sectionTitle:str(l.sectionTitle),sectionPath:Array.isArray(l.sectionPath)?l.sectionPath.filter((p):p is string=>typeof p==='string'):[],terms:Array.isArray(l.terms)?l.terms.filter((t):t is string=>typeof t==='string'):[],score:num(l.score),status:typeof l.status==='string'?l.status:undefined};
    });
  }
  return {
    version:str(c.version),serverTime:typeof c.serverTime==='string'?c.serverTime:undefined,
    choices:questionsRaw.map(normalizeQuestion),
    materials:(Array.isArray(c.materials)?c.materials:[]).map(normalizeMaterial),
    cases:(Array.isArray(c.cases)?c.cases:[]).map(normalizeCase),
    essays:(Array.isArray(c.essays)?c.essays:[]).map(normalizeEssay),
    lessons:Array.isArray(c.lessons)?c.lessons as Catalog['lessons']:[],
    knowledgeLinks,
  };
}

export function normalizeSession(raw:unknown):ServerSession|null{
  const wrapper=record(raw)?raw:{};
  const s=record(wrapper.session)?wrapper.session:record(raw)?raw:null;
  if(!s||typeof s.id!=='string') return null;
  const answers:ServerSession['answers']={};
  if(record(s.answers)) for(const [questionId,value] of Object.entries(s.answers)){
    if(!record(value)) continue;
    answers[questionId]={
      selected:(['A','B','C','D'].includes(str(value.selected))?str(value.selected):undefined) as AnswerStateSelected,
      submitted:value.submitted===true,assisted:value.assisted===true,
      attemptId:typeof value.attemptId==='string'?value.attemptId:undefined,
      correct:typeof value.correct==='boolean'?value.correct:undefined,
      revealed:typeof value.revealed==='boolean'?value.revealed:undefined,
      pending:value.pending===true,
      solution:record(value.solution)?value.solution as ServerSession['answers'][string]['solution']:undefined,
    };
  }
  const status=str(s.status,'active');
  return {
    id:s.id,mode:(['chapter','all','exam','wrong','favorites','review'].includes(str(s.mode))?str(s.mode):'all') as ServerSession['mode'],
    scope:record(s.scope)?s.scope as ServerSession['scope']:{},
    questionIds:Array.isArray(s.questionIds)?s.questionIds.filter((id):id is string=>typeof id==='string'):[],
    revisions:record(s.revisions)?s.revisions as Record<string,string>:undefined,
    index:num(s.index,0),elapsedSeconds:num(s.elapsedSeconds,num(s.elapsed,0)),status,bankVersion:str(s.bankVersion),
    version:num(s.version,1),createdAt:str(s.createdAt),updatedAt:str(s.updatedAt),
    deadlineAt:typeof s.deadlineAt==='string'?s.deadlineAt:null,
    submittedAt:typeof s.submittedAt==='string'?s.submittedAt:null,
    score:typeof s.score==='number'?s.score:null,maxScore:typeof s.maxScore==='number'?s.maxScore:null,
    answers,finished:status!=='active',
  };
}
type AnswerStateSelected=import('./types').AnswerState['selected'];

export function normalizeMarks(raw:unknown):Mark[]{
  const wrapper=record(raw)?raw:{};
  const list=Array.isArray(wrapper.marks)?wrapper.marks:(Array.isArray(wrapper.items)?wrapper.items:[]);
  return list.filter(record).map((entry)=>{
    const m=entry as Record<string,unknown>;
    return {id:str(m.id||m.questionId),favorite:m.favorite===true,uncertain:m.uncertain===true,note:str(m.note),version:typeof m.version==='number'?m.version:undefined};
  }).filter(mark=>!!mark.id);
}

export function normalizeAnnotationList(raw:unknown):Record<string,unknown>[]{
  const wrapper=record(raw)?raw:{};
  const list=Array.isArray(wrapper.annotations)?wrapper.annotations:(Array.isArray(wrapper.items)?wrapper.items:[]);
  return list.filter(record) as Record<string,unknown>[];
}
