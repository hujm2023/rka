import {WritingHistory} from '../components/WritingHistory';
import {useEffect,useRef,useState} from 'react';
import {Link,useLocation} from '@tanstack/react-router';
import {ArrowLeft,ArrowRight,CheckCircle2,FileText,Layers,PenLine,Search,Sparkles,History} from 'lucide-react';
import {useLiveQuery} from 'dexie-react-hooks';
import {useApp} from '../context';
import {apiGet,apiPost,apiPut,ApiError,errorText} from '../api';
import {enqueue} from '../outbox';
import {syncDb} from '../sync';
import {db} from '../db';
import type {Case,Essay,Draft,WritingDocument} from '../types';
import {modules,sourceTypeLabel} from '../types';
import {Badge,Button,Empty,Loading,Markdown,Modal,PageTitle,SourceLink} from '../ui';

type WritingKind='case'|'essay';
type ConflictState={serverVersion:number;serverValues:Record<string,string>;clientValues:Record<string,string>;serverUpdatedAt?:string}|null;

const documentOf=(raw:unknown):WritingDocument|null=>{
  const wrapper=raw&&typeof raw==='object'?raw as Record<string,unknown>:{};
  const doc=(wrapper.document&&typeof wrapper.document==='object'?wrapper.document:wrapper) as Record<string,unknown>;
  if(!doc||typeof doc!=='object') return null;
  const values=doc.values&&typeof doc.values==='object'&&!Array.isArray(doc.values)?doc.values as Record<string,string>:{};
  return {id:String(doc.id||''),kind:(String(doc.kind)==='essay'?'essay':'case'),targetId:String(doc.targetId||''),values,version:typeof doc.version==='number'?doc.version:0,updatedAt:typeof doc.updatedAt==='string'?doc.updatedAt:undefined};
};

function useDocument(kind:WritingKind,targetId:string){
  const [values,setValues]=useState<Record<string,string>>({});
  const [version,setVersion]=useState(0);
  const [loaded,setLoaded]=useState(false);
  const [status,setStatus]=useState('正在恢复草稿…');
  const [conflict,setConflict]=useState<ConflictState>(null);
  const [serverError,setServerError]=useState('');
  const valuesRef=useRef<Record<string,string>>({}),versionRef=useRef(0),timer=useRef<number|undefined>(undefined),latest=useRef(0);
  const applyValues=(next:Record<string,string>)=>{valuesRef.current=next;setValues(next);};
  const applyVersion=(next:number)=>{versionRef.current=next;setVersion(next);};

  useEffect(()=>{
    let alive=true;
    (async()=>{
      let local:Draft|undefined;
      try{local=await db.drafts.get(targetId);}catch{/* local cache optional */}
      if(!alive) return;
      if(local){applyValues(local.values);applyVersion(0);setStatus('已保存到本机');}
      setLoaded(true);
      try{
        const raw=await apiGet<unknown>(`/api/writing/${kind}/${encodeURIComponent(targetId)}`);
        const remote=documentOf(raw);
        if(!alive) return;
        if(remote&&remote.version>0){
          const remoteTime=remote.updatedAt?Date.parse(remote.updatedAt):0;
          const localTime=local?.updatedAt?Date.parse(local.updatedAt):0;
          if(!local||remoteTime>=localTime){applyValues(remote.values);applyVersion(remote.version);setStatus('已保存到服务器');}
          else{applyVersion(remote.version);setStatus('本地草稿较新，等待保存');setTimeout(()=>void persist(valuesRef.current,remote.version),800);}
        }else if(remote){applyVersion(remote.version);}
      }catch(caught){
        if(!alive) return;
        setServerError(errorText(caught));
        if(!local)setStatus('已保存到本机（服务器草稿暂不可用）');
      }
    })();
    return()=>{alive=false;window.clearTimeout(timer.current);};
  },[kind,targetId]);

  async function persist(nextValues:Record<string,string>,useVersion?:number){
    const baseVersion=useVersion??versionRef.current;
    const current=++latest.current;
    setStatus('正在保存…');
    try{
      const raw=await apiPut<unknown>(`/api/writing/${kind}/${encodeURIComponent(targetId)}`,{values:nextValues,version:baseVersion});
      const remote=documentOf(raw);
      if(current!==latest.current) return;
      applyVersion(remote?.version??baseVersion+1);setStatus('已保存到服务器');setServerError('');
      await db.drafts.put({id:targetId,kind,values:nextValues,updatedAt:new Date().toISOString()});
    }catch(caught){
      if(current!==latest.current) return;
      if(caught instanceof ApiError&&caught.status===409){
        const details=(caught.details||{}) as Record<string,unknown>;
        const serverValues=(details.serverValues&&typeof details.serverValues==='object'?details.serverValues:{}) as Record<string,string>;
        const clientValues=(details.clientValues&&typeof details.clientValues==='object'?details.clientValues:nextValues) as Record<string,string>;
        const serverVersion=typeof details.serverVersion==='number'?details.serverVersion:baseVersion+1;
        setConflict({serverVersion,serverValues,clientValues});
        setStatus('版本冲突：请选择保留哪一版');
        await syncDb.conflicts.put({id:`${kind}:${targetId}:${Date.now()}`,kind,title:`${kind === 'case' ? '案例' : '论文'} ${targetId}`,clientValues,serverValues,serverVersion,message:errorText(caught),createdAt:new Date().toISOString()});
      }else if(caught instanceof ApiError&&caught.status===0){
        setStatus('待同步');
        await enqueue({dedupeKey:`writing:${kind}:${targetId}`,kind:'writing',method:'PUT',path:`/api/writing/${kind}/${encodeURIComponent(targetId)}`,body:{values:nextValues,version:baseVersion}});
      }else{
        setStatus('已保存到本机');
        setServerError(`服务器暂未接受保存：${errorText(caught)}`);
      }
    }
  }

  const set=(key:string,value:string)=>{
    const next={...valuesRef.current,[key]:value};
    applyValues(next);setStatus('正在保存…');
    void db.drafts.put({id:targetId,kind,values:next,updatedAt:new Date().toISOString()}).catch(()=>{});
    window.clearTimeout(timer.current);
    timer.current=window.setTimeout(()=>void persist(valuesRef.current),700);
  };

  async function keepMine(){
    const mine=valuesRef.current;
    const serverVersion=conflict?.serverVersion??versionRef.current;
    setConflict(null);setStatus('正在保存…');
    await persist(mine,serverVersion);
  }
  function useServer(){
    if(!conflict) return;
    applyValues(conflict.serverValues);applyVersion(conflict.serverVersion);setStatus('已使用服务器版本');setConflict(null);
    void db.drafts.put({id:targetId,kind,values:conflict.serverValues,updatedAt:new Date().toISOString()});
  }

  async function restore(values:Record<string,string>){
    const current=documentOf(await apiGet(`/api/writing/${kind}/${encodeURIComponent(targetId)}`));
    applyValues(values);
    await db.drafts.put({id:targetId,kind,values,updatedAt:new Date().toISOString()});
    await persist(values,current?.version??0);
  }
  return {values,version,loaded,status,conflict,serverError,set,keepMine,useServer,restore};
}

export function Writing({kind}:{kind:WritingKind}){
  const {catalog}=useApp(),location=useLocation();
  const {id}=location.search as {id?:string};
  const [query,setQuery]=useState(''),[source,setSource]=useState('all');
  const drafts=useLiveQuery(()=>db.drafts.where('updatedAt').above('').toArray(),[],[])||[];
  const items=kind==='case'?catalog.cases:catalog.essays;const item=items.find(entry=>entry.id===id);
  const title=kind==='case'?'把分析，写成有依据的答案。':'把项目经验，写成架构思考。';
  const filtered=items.filter(entry=>(source==='all'||entry.sourceType===source)&&`${entry.title} ${entry.term} ${modules[entry.module]}`.toLowerCase().includes(query.toLowerCase())).sort((a,b)=>b.term.localeCompare(a.term,'zh'));
  if(item)return kind==='case'?<CaseEditor key={item.id} item={item as Case}/>:<EssayEditor key={item.id} item={item as Essay}/>;
  return <div className="page-enter"><PageTitle eyebrow={kind==='case'?'CASE WORKSHOP':'ESSAY WORKSHOP'} title={title} description={kind==='case'?'先独立作答，再对照参考要点。AI 帮你发现遗漏，练习更准确的表达。':'从审题、项目素材到提纲与正文，慢慢打磨一篇属于你的论文。'}/><div className="writing-intro"><div className="intro-icon">{kind==='case'?<Layers size={29}/>:<PenLine size={29}/>}</div><div><h3>{kind==='case'?'理解场景 → 拆解问题 → 分点作答':'真实项目 → 清晰提纲 → 具体论述'}</h3><p>{kind==='case'?`${items.length} 道案例，参考答案仅供学习；部分历史资料含 AI 补充内容。`:`${items.length} 道论文题，草稿保存在服务器并保留版本历史。`}</p></div><Badge tone="green">{drafts.filter(draft=>draft.kind===kind).length} 份草稿</Badge></div><div className="library-toolbar"><div className="search-field"><Search size={17}/><input aria-label="搜索写作题目" placeholder="搜索主题、年份或领域…" value={query} onChange={event=>setQuery(event.target.value)}/></div><select aria-label="筛选写作题来源" value={source} onChange={event=>setSource(event.target.value)}><option value="all">全部来源</option><option value="real">真题 / 回忆版</option><option value="mock">模拟题</option><option value="ai">AI 生成练习</option></select><span>{filtered.length} 个题目</span></div><div className="writing-grid">{filtered.map((entry,index)=>{const draft=drafts.find(saved=>saved.id===entry.id);return <Link to={kind==='case'?'/cases':'/essays'} search={{id:entry.id}} className="panel writing-card" key={entry.id}><div className="writing-card-top"><span className="tiny-caps">{kind==='case'?'CASE':'ESSAY'} / {String(index+1).padStart(2,'0')}</span><Badge>{sourceTypeLabel(entry.sourceType)}</Badge></div><h3>{entry.title||modules[entry.module]}</h3><p>{entry.term}</p><div className="writing-card-bottom"><span>{draft?<><CheckCircle2 size={14}/> 已有草稿</>:<><FileText size={14}/>{modules[entry.module]||'综合应用'}</>}</span><ArrowRight size={17}/></div></Link>;})}</div>{!filtered.length&&<Empty title="没有匹配的题目">试试其他关键词或来源。</Empty>}</div>;
}

function ConflictDialog({conflict,onKeep,onUseServer}:{conflict:ConflictState;onKeep:()=>void;onUseServer:()=>void}){
  if(!conflict) return null;
  const clientKeys=new Set(Object.keys(conflict.clientValues||{}));
  const serverKeys=new Set(Object.keys(conflict.serverValues||{}));
  const changed=[...new Set([...clientKeys,...serverKeys])].filter(key=>(conflict.clientValues?.[key]||'')!==(conflict.serverValues?.[key]||''));
  return <Modal open onOpenChange={()=>{/* the choice must be explicit */}} title="写作版本冲突" description="服务器上的稿件已被其他设备更新。双方内容都已保留，请选择接下来使用哪一版。"><div className="conflict-summary"><p>冲突字段：{changed.length?changed.join('、'):'整体内容'}。服务器版本 v{conflict.serverVersion}。</p></div><div className="conflict-columns"><div><h4>我的版本</h4>{changed.slice(0,3).map(key=><div key={key}><strong>{key}</strong><p>{(conflict.clientValues[key]||'（空）').slice(0,220)}</p></div>)}</div><div><h4>服务器版本</h4>{changed.slice(0,3).map(key=><div key={key}><strong>{key}</strong><p>{(conflict.serverValues[key]||'（空）').slice(0,220)}</p></div>)}</div></div><div className="modal-actions"><Button variant="secondary" onClick={onUseServer}>使用服务器版本</Button><Button onClick={onKeep}>保留我的版本并覆盖</Button></div></Modal>;
}

function ReviewList({kind,id}:{kind:WritingKind;id:string}){
  const [reviews,setReviews]=useState<Record<string,unknown>[]>([]),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const load=async()=>{try{const raw=await apiGet<unknown>(`/api/writing/${kind}/${encodeURIComponent(id)}/reviews`);const wrapper=raw&&typeof raw==='object'?raw as Record<string,unknown>:{};const list=Array.isArray(wrapper.reviews)?wrapper.reviews:Array.isArray(wrapper.items)?wrapper.items:[];setReviews(list.filter((entry):entry is Record<string,unknown>=>!!entry&&typeof entry==='object'));}catch(caught){setError(errorText(caught));}};
  useEffect(()=>{void load();},[kind,id]);
  async function create(){setBusy(true);setError('');try{await apiPost(`/api/writing/${kind}/${encodeURIComponent(id)}/reviews`,{});await load();}catch(caught){setError(`创建点评任务失败：${errorText(caught)}`);}finally{setBusy(false);}}
  return <section className="panel reviews-panel"><div className="section-heading"><h3><History size={17}/> AI 点评记录</h3><Button variant="secondary" disabled={busy} onClick={()=>void create()}>{busy?'正在创建…':'请 AI 点评当前版本'}</Button></div>{error&&<div className="inline-error" role="alert">{error}</div>}{reviews.length?<div className="review-list">{reviews.map((review,index)=><div className="review-item" key={String(review.id||index)}><div><strong>绑定版本 v{String(review.documentVersion??review.version??'—')}</strong><Badge tone={review.status==='completed'?'green':'neutral'}>{String(review.status||'排队中')}</Badge></div>{typeof review.summary==='string'&&<p>{review.summary}</p>}</div>)}</div>:<p className="muted-text">还没有点评记录。保存草稿后可请 AI 针对当前版本给出反馈；采纳前请先核对版本。</p>}</section>;
}

function CaseEditor({item}:{item:Case}){
  const {setAI}=useApp();const document=useDocument('case',item.id);
  const ask=(index?:number)=>setAI({id:`case:${item.id}:${index??'all'}`,kind:'case',title:item.title,module:item.module,documentId:item.id,documentVersion:document.version,text:`案例背景：${item.description||''}\n${item.subQuestions.map((question,questionIndex)=>index===undefined||index===questionIndex?`问题 ${questionIndex+1}：${question.prompt}\n我的回答：${document.values[`answer-${questionIndex}`]||'尚未作答，请引导审题而非给完整答案'}\n第三方参考答案：${question.referenceAnswer||''}`:'').join('\n')}`});
  return <div className="page-enter"><Link to="/cases" search={{}} className="text-link back-link"><ArrowLeft size={15}/> 返回案例题库</Link><PageTitle eyebrow="CASE WORKSHOP" title={item.title||'案例分析练习'} description={`${item.term} · ${modules[item.module]||'综合应用'}`} action={<span className="save-indicator"><i/>{document.status}</span>}/>{document.serverError&&<div className="inline-error" role="alert">{document.serverError}</div>}<div className="case-layout"><section className="panel case-background"><div className="section-heading"><h3>案例背景</h3><Badge>{sourceTypeLabel(item.sourceType)}</Badge></div><Markdown>{item.description||''}</Markdown><SourceLink href={item.sourceUrl}/></section><div className="case-answers">{!document.loaded?<Loading/>:item.subQuestions.map((question,index)=><section className="panel case-question" key={index}><span className="eyebrow">QUESTION {String(index+1).padStart(2,'0')}</span><Markdown>{question.prompt}</Markdown><label>我的作答<textarea aria-label={`问题 ${index+1} 的回答`} value={document.values[`answer-${index}`]||''} onChange={event=>document.set(`answer-${index}`,event.target.value)} rows={7} placeholder="先独立分析，用关键词和分点结构写下你的答案…"/></label><div className="answer-editor-tools"><small>{(document.values[`answer-${index}`]||'').replace(/\s/g,'').length} 字</small><Button variant="secondary" onClick={()=>ask(index)}><Sparkles size={15}/> AI 点评这段回答</Button></div><details className="reference-answer"><summary>对照第三方参考答案</summary><p className="muted-text">原资料可能含 AI 补充答案，仅供复习参考，不作为官方评分标准。</p><Markdown>{question.referenceAnswer||'原资料未提供该问题的参考答案。'}</Markdown></details></section>)}</div></div><WritingHistory kind="case" id={item.id} restore={document.restore}/><ReviewList kind="case" id={item.id}/><ConflictDialog conflict={document.conflict} onKeep={()=>void document.keepMine()} onUseServer={document.useServer}/></div>;
}

function EssayEditor({item}:{item:Essay}){
  const {setAI}=useApp();const document=useDocument('essay',item.id);const [tab,setTab]=useState('project');
  const tabs=[['project','项目素材'],['outline','论文提纲'],['abstract','摘要'],['body','正文']] as const;
  const placeholder:Record<string,string>={project:'业务背景：\n我承担的职责：\n关键架构决策：\n遇到的问题与取舍：\n能够证实的实施效果：\n\n只填写你实际经历的项目。缺少指标可以先留空。',outline:'逐项拆解题目要求，列出准备论述的要点与项目例子…',abstract:'概述项目背景、你的职责、论文主题和关键实践…',body:'结合实际项目，说明架构决策、实施过程、问题解决与反思…'};
  const total=(document.values.abstract||'').replace(/\s/g,'').length+(document.values.body||'').replace(/\s/g,'').length;
  const ask=()=>setAI({id:`essay:${item.id}`,kind:'essay',title:item.title,module:item.module,documentId:item.id,documentVersion:document.version,text:`论文题目与要求：${item.prompt}\n当前编辑阶段：${tabs.find(entry=>entry[0]===tab)?.[1]}\n${tabs.map(([key,label])=>`${label}：${document.values[key]||'尚未填写'}`).join('\n')}\n参考写作要点（仅参考，非官方标准）：${item.writingPoints.slice(0,4000)}`});
  return <div className="page-enter"><Link to="/essays" search={{}} className="text-link back-link"><ArrowLeft size={15}/> 返回论文题库</Link><PageTitle eyebrow="ESSAY WORKSHOP" title={item.title} description={`${item.term} · ${sourceTypeLabel(item.sourceType)}`} action={<span className="save-indicator"><i/>{document.status}</span>}/>{document.serverError&&<div className="inline-error" role="alert">{document.serverError}</div>}<div className="essay-layout"><aside><section className="panel essay-prompt"><h3>题目与要求</h3><Markdown>{item.prompt}</Markdown><SourceLink href={item.sourceUrl}/></section><section className="essay-tip"><PenLine size={24}/><h3>让每个论点，都有项目支撑。</h3><p>写清楚你为何这样选择、如何实施，以及实际发生了什么。</p><Button variant="secondary" disabled={!document.loaded} onClick={ask}><Sparkles size={16}/> 请 AI 帮我审阅</Button></section></aside><section className="panel essay-editor"><div className="editor-tabs" role="tablist" aria-label="论文写作阶段">{tabs.map(([key,title])=><button role="tab" aria-selected={tab===key} className={tab===key?'active':''} key={key} onClick={()=>setTab(key)}>{title}{document.values[key]?.trim()&&<span/>}</button>)}</div><div className="editor-top"><span>{tabs.find(entry=>entry[0]===tab)?.[1]}</span><small>{(document.values[tab]||'').replace(/\s/g,'').length} 字 · 摘要与正文共 {total} 字</small></div>{document.loaded?<textarea className="essay-textarea" aria-label={tabs.find(entry=>entry[0]===tab)?.[1]} value={document.values[tab]||''} onChange={event=>document.set(tab,event.target.value)} placeholder={placeholder[tab]} spellCheck={false}/>:<Loading/>}<div className="editor-footer"><span>字数仅作辅助统计，具体要求请以试题为准。</span><Button variant="secondary" disabled={!document.loaded} onClick={ask}><Sparkles size={15}/> AI 写作教练</Button></div><details className="reference-answer essay-reference"><summary>参考写作要点与训练示例</summary><p className="muted-text">保留来源资料的建议与示例，不代表真实项目经历或官方范文。</p><Markdown>{item.writingPoints}</Markdown></details></section></div><WritingHistory kind="essay" id={item.id} restore={document.restore}/><ReviewList kind="essay" id={item.id}/><ConflictDialog conflict={document.conflict} onKeep={()=>void document.keepMine()} onUseServer={document.useServer}/></div>;
}
