import {useEffect,useMemo,useRef,useState} from 'react';
import {useLocation,useNavigate,Link} from '@tanstack/react-router';
import {ArrowLeft,ArrowRight,Bookmark,Check,CheckCircle2,ChevronLeft,ChevronRight,Clock3,Flag,Layers,Lightbulb,ListChecks,RotateCcw,Search,SlidersHorizontal,Sparkles,XCircle,CloudOff} from 'lucide-react';
import {useApp} from '../context';
import {apiGet,apiPatch,apiPost,apiPut,ApiError,errorText,normalizeSession,serverNow,syncServerClock} from '../api';
import {cacheGet,cachePut} from '../sync';
import {enqueue,onOutbox} from '../outbox';
import {db} from '../db';
import {buildQueue,fmtTime,percent} from '../study';
import {newId} from '../id';
import {defaultFilters,modules,sourceTypeLabel,type Filters,type Mark,type Option,type Question,type ServerSession,type Solution} from '../types';
import {Badge,Button,Empty,Loading,Markdown,Modal,PageTitle,SourceLink} from '../ui';
import {QuestionKnowledgeLinks} from '../components/LearningLinks';
import {SyncStatus} from '../components/SyncStatus';

const modes=[['all','自由练习'],['chapter','章节突破'],['exam','真题套卷'],['wrong','错题巩固'],['favorites','我的收藏'],['review','到期复习']] as const;

// Unsubmitted selections live only in the local cache until the answer is
// submitted; the server owns submitted answers and the current position. Merge
// the local-only state back after a reload so a not-yet-submitted choice is not
// lost, without ever overriding a server-confirmed answer.
function mergeLocalAnswerStates(server:ServerSession|null,cached:ServerSession|undefined|null):ServerSession|null{
  if(!server||!cached||cached.id!==server.id) return server;
  const answers={...server.answers};
  for(const [questionId,local] of Object.entries(cached.answers||{})){
    if(!local||answers[questionId]?.submitted) continue;
    if(!local.selected&&!local.assisted) continue;
    answers[questionId]={...answers[questionId],submitted:false,selected:local.selected??answers[questionId]?.selected,assisted:local.assisted||answers[questionId]?.assisted||false};
  }
  return {...server,answers};
}

type ExamResult={correct?:boolean;answer?:Option;solution?:Solution};

export function Practice(){
  const {catalog,catalogReady,catalogError,reloadCatalog,attempts,marks:contextMarks,reading,notify,setAI}=useApp();
  const location=useLocation(),navigate=useNavigate();const search=location.search as Record<string,string>;
  const [filters,setFilters]=useState<Filters>({...defaultFilters,...(search.mode?{mode:search.mode as Filters['mode']}:{}),...(search.module?{module:search.module,mode:'chapter'}:{}),...(search.material?{materialId:search.material}:{}),...(search.section?{sectionId:search.section}:{})});
  const [session,setSession]=useState<ServerSession|null|undefined>(undefined);
  const [showSetup,setShowSetup]=useState(false);
  const [confirm,setConfirm]=useState(false);
  const [conflictSession,setConflictSession]=useState<ServerSession|null>(null);
  const [saving,setSaving]=useState(false);
  const [pageError,setPageError]=useState('');
  const [marks,setMarks]=useState<Mark[]>(contextMarks);
  const [markConflict,setMarkConflict]=useState<{questionId:string;message:string;serverValues?:Record<string,unknown>;serverVersion?:number}|null>(null);
  const [examResults,setExamResults]=useState<Record<string,ExamResult>>({});
  const [examSubmitted,setExamSubmitted]=useState(false);
  const [remaining,setRemaining]=useState<number|null>(null);
  const [clockOffset,setClockOffset]=useState(0);
  const sessionRef=useRef<ServerSession|null>(null),setupRef=useRef(showSetup),started=useRef(Date.now()),submitLock=useRef(false),startBusy=useRef(false),autoHandled=useRef(''),noteTimer=useRef<number|undefined>(undefined);
  useEffect(()=>{setMarks(contextMarks);},[contextMarks]);
  const pendingChoices=useRef(0);
  useEffect(()=>{setupRef.current=showSetup;},[showSetup]);

  useEffect(()=>{
    let alive=true;
    (async()=>{
      try{
        const raw=await apiGet<unknown>('/api/practice/sessions/active');
        const active=normalizeSession(raw);
        const cached=await cacheGet<ServerSession>('session:active');
        if(!alive) return;
        const merged=mergeLocalAnswerStates(active,cached?.value);
        sessionRef.current=merged;setSession(merged);
        if(merged) await cachePut('session:active',merged);
      }catch(caught){
        const cached=await cacheGet<ServerSession>('session:active');
        if(!alive) return;
        if(cached?.value){sessionRef.current=cached.value;setSession(cached.value);notify('网络不可用：已从本地缓存恢复练习现场。');}
        else{setSession(null);setPageError(`练习现场读取失败：${errorText(caught)}`);}
      }
      const offset=await syncServerClock();if(alive) setClockOffset(offset);
    })();
    return()=>{alive=false;};
  },[]);

  useEffect(()=>{if(search.mode||search.module||search.material){setShowSetup(true);if(search.start!=='1')setFilters({...defaultFilters,mode:(search.mode as Filters['mode'])||(search.material?'chapter':'all'),module:search.module||'all',...(search.material?{materialId:search.material}:{}),...(search.section?{sectionId:search.section}:{})});}},[search.mode,search.module,search.material,search.section,search.start]);

  const update=(next:ServerSession)=>{sessionRef.current=next;setSession(next);void cachePut('session:active',next);};
  const persistPosition=async(next:ServerSession)=>{
    try{const raw=await apiPatch<unknown>(`/api/practice/sessions/${next.id}`,{index:next.index,elapsedSeconds:next.elapsedSeconds,version:next.version});const authoritative=normalizeSession(raw);if(authoritative&&sessionRef.current?.id===next.id&&authoritative.version>=sessionRef.current.version){
        const latest=sessionRef.current;
        const merged=mergeLocalAnswerStates(authoritative,latest)!;
        // A position acknowledgement must not erase choices made while it was in flight.
        if(latest.index!==next.index)merged.index=latest.index;
        merged.elapsedSeconds=Math.max(merged.elapsedSeconds,latest.elapsedSeconds);
        update(merged);
      }}
    catch(caught){
      if(caught instanceof ApiError&&caught.status===409){await reloadSession(next.id,'学习位置已在另一台设备更新，已恢复服务器现场。');}
    }
  };
  async function reloadSession(id:string,message?:string){
    try{const raw=await apiGet<unknown>(`/api/practice/sessions/${id}`);const next=normalizeSession(raw);if(next&&sessionRef.current?.id===id){update(mergeLocalAnswerStates(next,sessionRef.current)!);if(message)notify(message);}}
    catch{/* offline keeps local position */}
  }

  useEffect(()=>{
    const tick=setInterval(()=>{
      const current=sessionRef.current;
      if(!current||current.finished||document.hidden||setupRef.current||pendingChoices.current) return;
      const next={...current,elapsedSeconds:current.elapsedSeconds+1};
      sessionRef.current=next;setSession(next);
      if(next.elapsedSeconds%10===0) void persistPosition(next);
    },1000);
    return()=>{clearInterval(tick);const current=sessionRef.current;if(current&&!current.finished)void persistPosition(current);};
  },[]);

  useEffect(()=>{
    const current=sessionRef.current;
    if(!current?.deadlineAt||current.finished){setRemaining(null);return;}
    const deadline=Date.parse(current.deadlineAt);
    const updateRemaining=()=>setRemaining(Math.max(0,Math.floor((deadline-serverNow())/1000)));
    updateRemaining();
    const timer=setInterval(updateRemaining,1000);
    return()=>clearInterval(timer);
  },[session?.deadlineAt,session?.finished,clockOffset]);

  useEffect(()=>onOutbox(async event=>{
    if(event.type!=='synced') return;
    if(event.record.kind==='answer'){
      const body=event.record.body as {questionId?:string;selected?:Option;id?:string};
      if(!body.questionId||!body.selected) return;
      const data=event.data as {correct?:boolean;revealed?:boolean;solution?:Solution;session?:unknown};
      const serverSession=normalizeSession(data.session);
      const base=serverSession||sessionRef.current;
      if(base){
        const next={...base,answers:{...base.answers,[body.questionId]:{...base.answers[body.questionId],submitted:true,selected:body.selected,correct:data.correct,revealed:data.revealed!==false,solution:data.solution,pending:false,sync:'synced' as const}}};
        update(next);
      }
      await db.attempts.put({id:event.record.id,questionId:body.questionId,revision:'',answer:body.selected,correct:data.correct===true,assisted:false,createdAt:event.record.createdAt,durationMs:0});
      notify('离线作答已同步到服务器。');
    }
  }),[]);

  const candidates=useMemo(()=>buildQueue(catalog.choices,attempts,marks,{...filters,...(filters.mode==='exam'?{source:'real'}:{})},catalog.knowledgeLinks),[catalog.choices,catalog.knowledgeLinks,attempts,marks,filters]);
  const q=session?catalog.choices.find(question=>question.id===session.questionIds[session.index]):undefined;
  const answer=q&&session?session.answers[q.id]||{}:{};
  const mark=marks.find(item=>item.id===q?.id);
  const solution:Solution|undefined=(q&&session?session.answers[q.id]?.solution:undefined)||examResults[q?.id||'']?.solution;
  const revealed=!!answer.submitted&&answer.revealed===true&&(answer.pending!==true);
  const correctNow=typeof answer.correct==='boolean'?answer.correct:(q&&solution?.answer?answer.selected===solution.answer:undefined);
  const isExam=session?.mode==='exam';

  useEffect(()=>{started.current=Date.now();setAI(null);const frame=requestAnimationFrame(()=>{const element=document.querySelector('.question-topline');if(element)window.scrollTo({top:Math.max(0,element.getBoundingClientRect().top+window.scrollY-25),behavior:'instant'});});return()=>cancelAnimationFrame(frame);},[q?.id,setAI]);

  async function choose(key:Option){
    const current=sessionRef.current;
    if(!current||!q||current.answers[q.id]?.submitted)return;
    const next={...current,answers:{...current.answers,[q.id]:{...current.answers[q.id],selected:key}}};
    sessionRef.current=next;pendingChoices.current++;
    try{
      // A visible selected option acknowledges a durable local draft, so an
      // immediate refresh cannot interrupt its IndexedDB transaction.
      await cachePut('session:active',next);
      if(sessionRef.current===next)setSession(next);
    }catch(caught){
      if(sessionRef.current===next)setSession(next);
      setPageError(`选择暂未保存到本地：${errorText(caught)}`);
    }finally{pendingChoices.current--;}
  }
  function move(delta:number){const current=sessionRef.current;if(!current||saving)return;const index=current.index+delta;if(index>=0&&index<current.questionIds.length){update({...current,index});void persistPosition({...current,index});}}

  async function submit(){
    const current=sessionRef.current;
    if(!current||!q||submitLock.current)return;
    const state=current.answers[q.id];
    if(!state?.selected||state.submitted)return;
    submitLock.current=true;setSaving(true);
    const eventId=newId(),selected=state.selected,durationMs=Date.now()-started.current,answeredAt=new Date().toISOString();
    const optimistic={...current,answers:{...current.answers,[q.id]:{...state,submitted:true,attemptId:eventId,pending:true,sync:'pending' as const}}};
    update(optimistic);
    const payload={id:eventId,questionId:q.id,selected,assisted:!!state.assisted,durationMs,answeredAt};
    try{
      const raw=await apiPost<{attempt?:unknown;correct?:boolean;revealed?:boolean;solution?:Solution;session?:unknown}>(`/api/practice/sessions/${current.id}/answers`,payload);
      const serverSession=normalizeSession(raw.session);
      const base=serverSession||sessionRef.current||current;
      const next={...base,answers:{...base.answers,[q.id]:{...base.answers[q.id],submitted:true,selected,attemptId:eventId,correct:raw.correct,revealed:raw.revealed!==false,solution:raw.solution,pending:false,sync:'synced' as const}}};
      update(next);
      await db.attempts.put({id:eventId,questionId:q.id,revision:q.revision,answer:selected,correct:raw.correct===true,assisted:!!state.assisted,createdAt:answeredAt,durationMs});
    }catch(caught){
      if(caught instanceof ApiError&&caught.status===0){
        await enqueue({id:eventId,kind:'answer',method:'POST',path:`/api/practice/sessions/${current.id}/answers`,body:payload});
        update({...optimistic,answers:{...optimistic.answers,[q.id]:{...optimistic.answers[q.id],sync:'pending' as const}}});
        notify('网络不可用：作答已保存到待同步队列，恢复网络后自动提交。');
      }else if(caught instanceof ApiError&&caught.status===409){
        update({...current,answers:{...current.answers,[q.id]:{...state,submitted:false}}});
        await reloadSession(current.id,'练习现场已在服务器更新，请重新作答本题。');
        setPageError(errorText(caught));
      }else{
        update({...current,answers:{...current.answers,[q.id]:{...state,submitted:false}}});
        setPageError(`作答提交失败：${errorText(caught)}`);
      }
    }finally{submitLock.current=false;setSaving(false);}
  }

  async function start(replace=false){
    if(startBusy.current)return;
    startBusy.current=true;setPageError('');
    const scope:Record<string,string|boolean>={source:filters.source,query:filters.query,includeMissing:filters.includeMissing};
    if(filters.materialId) scope.materialId=filters.materialId;
    if(filters.sectionId) scope.sectionId=filters.sectionId;
    if(filters.module&&filters.module!=='all') scope.module=filters.module;
    if(filters.term&&filters.term!=='all') scope.term=filters.term;
    const body:{mode:string;scope:Record<string,string|boolean>;count:number;replace?:boolean}={mode:filters.mode,scope,count:filters.mode==='exam'?Math.max(candidates.length,1):filters.count,...(replace?{replace:true}:{})};
    try{
      const raw=await apiPost<unknown>('/api/practice/sessions',body);
      const next=normalizeSession(raw);
      if(!next) throw new Error('服务端未返回练习现场。');
      update(next);setShowSetup(false);setConfirm(false);setConflictSession(null);setExamResults({});setExamSubmitted(false);
      void navigate({to:'/practice',search:{},replace:true});
      notify('练习现场已创建，位置与作答会同步到服务器。');
    }catch(caught){
      if(caught instanceof ApiError&&caught.status===409){
        const details=caught.details as {session?:unknown}|undefined;
        const active=normalizeSession(details||caught.details);
        if(active){setConflictSession(active);setConfirm(true);}
        else setPageError(errorText(caught));
      }else setPageError(`创建练习失败：${errorText(caught)}`);
    }finally{startBusy.current=false;}
  }

  useEffect(()=>{
    if(search.start!=='1'||!search.material){autoHandled.current='';return;}
    if(session===undefined||!catalogReady||!candidates.length)return;
    const key=[search.material,search.section,search.mode].join(':');
    if(autoHandled.current===key)return;
    autoHandled.current=key;
    const scopeMatches=session&&!session.finished&&session.scope.materialId===filters.materialId&&(session.scope.sectionId||'')===(filters.sectionId||'')&&session.mode===filters.mode;
    if(scopeMatches){setShowSetup(false);void navigate({to:'/practice',search:{},replace:true});}
    else void start();
  },[session===undefined,search.start,search.material,search.section,search.mode,catalogReady,candidates.length]);

  async function updateMark(change:Partial<Mark>){
    if(!q)return;
    const current=marks.find(item=>item.id===q.id)||{id:q.id,favorite:false,uncertain:false,note:''};
    const next={...current,...change};
    setMarks(rows=>[...rows.filter(item=>item.id!==next.id),next]);
    try{
      const raw=await apiPut<unknown>(`/api/practice/marks/${q.id}`,{favorite:next.favorite,uncertain:next.uncertain,note:next.note,version:current.version??0});
      const applied=raw as {mark?:Mark;version?:number};
      if(applied?.mark) setMarks(rows=>[...rows.filter(item=>item.id!==next.id),applied.mark as Mark]);
      else if(typeof applied?.version==='number') setMarks(rows=>rows.map(item=>item.id===next.id?{...item,version:applied.version}:item));
    }catch(caught){
      if(caught instanceof ApiError&&caught.status===409){
        const details=caught.details as Record<string,unknown>|undefined;
        setMarkConflict({questionId:q.id,message:errorText(caught),serverValues:details?.serverValues as Record<string,unknown>|undefined,serverVersion:typeof details?.serverVersion==='number'?details.serverVersion:undefined});
      }else if(caught instanceof ApiError&&caught.status===0){
        await enqueue({dedupeKey:`mark:${q.id}`,kind:'mark',method:'PUT',path:`/api/practice/marks/${q.id}`,body:{favorite:next.favorite,uncertain:next.uncertain,note:next.note,version:current.version??0}});
      }else setPageError(`标记保存失败：${errorText(caught)}`);
    }
  }
  function markQuestion(change:Partial<Mark>){void updateMark(change);}
  function changeNote(note:string){
    if(!q)return;
    const current=marks.find(item=>item.id===q.id)||{id:q.id,favorite:false,uncertain:false,note:''};
    setMarks(rows=>[...rows.filter(item=>item.id!==current.id),{...current,note}]);
    window.clearTimeout(noteTimer.current);
    noteTimer.current=window.setTimeout(()=>{void updateMark({note});},600);
  }

  async function finish(){
    const current=sessionRef.current;if(!current)return;
    try{const raw=await apiPost<unknown>(`/api/practice/sessions/${current.id}/finish`,{});const next=normalizeSession(raw);update(next?{...next,finished:true}:{...current,finished:true});}
    catch(caught){setPageError(`结束练习失败：${errorText(caught)}`);}
  }

  async function submitExam(){
    const current=sessionRef.current;if(!current||!isExam)return;
    try{
      const raw=await apiPost<{session?:unknown;results?:unknown[];score?:number;maxScore?:number}>(`/api/practice/sessions/${current.id}/submit`,{});
      const next=normalizeSession(raw.session);
      if(next)update(next);
      const results=Array.isArray(raw.results)?raw.results:[];
      const mapped:Record<string,ExamResult>={};
      const mergedAnswers:ServerSession['answers']=next?{...next.answers}:{...(sessionRef.current?.answers||{})};
      for(const entry of results){
        if(!entry||typeof entry!=='object') continue;
        const item=entry as Record<string,unknown>;
        const questionId=typeof item.questionId==='string'?item.questionId:'';
        if(!questionId) continue;
        const answer=typeof item.answer==='string'&&['A','B','C','D'].includes(item.answer)?item.answer as Option:undefined;
        const solution:Solution|undefined=(answer||typeof item.analysis==='string')?{answer,analysis:typeof item.analysis==='string'?item.analysis:undefined,scoringRule:item.scoringRule&&typeof item.scoringRule==='object'?item.scoringRule as Record<string,unknown>:undefined}:undefined;
        mapped[questionId]={correct:typeof item.correct==='boolean'?item.correct:undefined,answer,solution};
        mergedAnswers[questionId]={...mergedAnswers[questionId],submitted:true,selected:typeof item.selected==='string'&&['A','B','C','D'].includes(item.selected)?item.selected as Option:mergedAnswers[questionId]?.selected,correct:typeof item.correct==='boolean'?item.correct:undefined,revealed:true,solution};
      }
      if(next){next.answers=mergedAnswers;next.score=next.score??(typeof raw.score==='number'?raw.score:null);next.maxScore=next.maxScore??(typeof raw.maxScore==='number'?raw.maxScore:null);}
      setExamResults(mapped);setExamSubmitted(true);setConfirm(false);
      notify('试卷已交卷，答案与得分由服务器确认。');
    }catch(caught){
      if(caught instanceof ApiError&&caught.status===409&&caught.code==='deadline_passed'){setPageError('已到服务器截止时间，交卷未被接受；以已经提交的作答为准。');await reloadSession(current.id);}
      else setPageError(`交卷失败：${errorText(caught)}`);
    }
  }

  function ai(hint:boolean){
    if(!q)return;
    const solutionText=solution?`\n参考答案：${solution.answer||''}\n解析：${solution.analysis||''}`:'';
    setAI({id:`question:${q.id}:${hint?'hint':'review'}`,kind:'question',title:`${q.term} · 第 ${q.questionNo} 题`,module:q.module,hint,questionId:q.id,questionRevision:q.revision,sessionId:session?.id,text:`题干：${q.stem}\n选项：${JSON.stringify(q.options)}${!hint&&revealed?solutionText:''}\n我的答案：${answer.selected||'未作答'}\n图示状态：${q.figureMissing?'缺图，不得推测图像关系':q.figure?JSON.stringify(q.figure):'无附加图示'}`,onHint:()=>{const current=sessionRef.current;if(current&&q&&!current.answers[q.id]?.submitted)update({...current,answers:{...current.answers,[q.id]:{...current.answers[q.id],assisted:true}}});}});
  }

  useEffect(()=>{
    const handler=(event:KeyboardEvent)=>{
      if(showSetup||!q||session?.finished||event.ctrlKey||event.metaKey||event.altKey||event.isComposing||document.querySelector('[role="dialog"]')||(event.target instanceof HTMLElement&&(event.target.matches('input,textarea,select')||event.target.isContentEditable)))return;
      if(['a','b','c','d','1','2','3','4'].includes(event.key.toLowerCase())){event.preventDefault();choose((/\d/.test(event.key)?'ABCD'[Number(event.key)-1]:event.key.toUpperCase()) as Option);}
      else if(event.key==='Enter'){if(event.target instanceof HTMLElement&&event.target.closest('button')&&!event.target.closest('[role=radio]'))return;event.preventDefault();void submit();}
      else if(event.key==='ArrowRight'){event.preventDefault();move(1);}
      else if(event.key==='ArrowLeft'){event.preventDefault();move(-1);}
    };
    window.addEventListener('keydown',handler);return()=>window.removeEventListener('keydown',handler);
  });

  if(!catalogReady) return <Loading/>;
  if(session===undefined) return <Loading/>;
  if(!catalog.choices.length) return <div className="page-enter"><PageTitle eyebrow="PRACTICE STUDIO" title="题库暂时无法加载。" description="练习功能需要服务器上的题库数据。"/><Empty title="内容服务尚未就绪" action={<Button onClick={reloadCatalog}>重新加载题库</Button>}>{catalogError||'请稍后重试，或联系管理员检查内容导入。'}</Empty></div>;

  const scopeMaterial=(catalog.materials||[]).find(material=>material.id===filters.materialId);
  const scopeSection=scopeMaterial?.sections.find(section=>section.id===filters.sectionId);
  if(showSetup||!session||!q){
    return <div className="page-enter">
      <PageTitle eyebrow="PRACTICE STUDIO" title="把每一道题，变成一次理解。" description="选择适合今天的练习方式。作答、标记和学习位置会保存到服务器。" action={session&&!session.finished?<Button variant="secondary" onClick={()=>setShowSetup(false)}>继续上次练习 <ArrowRight size={16}/></Button>:undefined}/>
      {pageError&&<div className="inline-error" role="alert">{pageError}<button onClick={()=>setPageError('')}>关闭</button></div>}
      {filters.materialId&&<div className="practice-scope-banner"><div><strong>{scopeMaterial?.title||'资料关联范围'}</strong><p>{scopeSection?`当前小节：${scopeSection.title}`:'只练习与本章原文关联的题目'} · {candidates.length} 道符合条件</p></div><Button variant="ghost" onClick={()=>setFilters({...filters,materialId:undefined,sectionId:undefined})}>清除章节范围</Button></div>}
      <div className="practice-mode-grid">{modes.map(([key,title],index)=><button className={`mode-tile ${filters.mode===key?'selected':''}`} key={key} onClick={()=>setFilters({...filters,mode:key})}><span className="mode-number">0{index+1}</span><h3>{title}</h3><p>{['按自己的节奏，练一组题','专注一个领域，逐个突破','按年份回顾，完成后复盘','重新理解，直到真正掌握','把重点题留给下一次','错题 1 天、辅助答对 2 天、独立答对 7 天'][index]}</p>{filters.mode===key&&<CheckCircle2 size={17}/>}</button>)}</div>
      <section className="panel setup-panel"><div className="section-heading"><h2><SlidersHorizontal size={19}/> 定制这次练习</h2><Badge tone="green">{candidates.length} 道符合条件</Badge></div>
        <div className="filter-grid">
          <label>知识领域<select value={filters.module} onChange={event=>setFilters({...filters,module:event.target.value})}><option value="all">全部领域</option>{Object.entries(modules).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>
          <label>考试年份 / 套卷<select value={filters.term} onChange={event=>setFilters({...filters,term:event.target.value})}><option value="all">{filters.mode==='exam'?'请选择一套试卷':'全部年份与试卷'}</option>{[...new Set(catalog.choices.filter(question=>filters.mode!=='exam'||question.sourceType==='real').map(question=>question.term))].sort((a,b)=>b.localeCompare(a,'zh')).map(term=><option key={term}>{term}</option>)}</select></label>
          <label>题目来源<select disabled={filters.mode==='exam'} value={filters.mode==='exam'?'real':filters.source} onChange={event=>setFilters({...filters,source:event.target.value})}><option value="all">全部来源</option><option value="real">真题 / 回忆版</option><option value="mock">模拟题</option><option value="ai">AI 生成练习</option></select></label>
          <label>本次题数<select disabled={filters.mode==='exam'} value={filters.mode==='exam'?0:filters.count} onChange={event=>setFilters({...filters,count:Number(event.target.value)})}>{filters.mode==='exam'&&<option value={0}>整套（{candidates.length} 道）</option>}{[10,20,50,75,3000].map(count=><option key={count} value={count}>{count===3000?'全部':`${count} 道`}</option>)}</select></label>
        </div>
        <div className="search-field"><Search size={17}/><input aria-label="搜索题目" placeholder="搜索题目或知识点，例如 ATAM、事务、微服务…" value={filters.query} onChange={event=>setFilters({...filters,query:event.target.value})}/></div>
        <label className="checkbox-label"><input type="checkbox" checked={filters.includeMissing} onChange={event=>setFilters({...filters,includeMissing:event.target.checked})}/>包含疑似缺图题（默认排除，查看时会提示）</label>
        <div className="setup-footer"><p>新练习会替换当前练习现场，历史作答与收藏始终保留在服务器。</p><Button disabled={!candidates.length||(filters.mode==='exam'&&filters.term==='all')||startBusy.current} onClick={()=>session&&!session.finished?setConfirm(true):void start()}>{startBusy.current?'正在创建…':'开始练习'} <ArrowRight size={17}/></Button></div>
      </section>
      <section className="panel source-note"><Layers size={17}/>题库保留真题、回忆版与模拟题的来源。参考答案可能存在疏漏；有疑问可标记并结合原始资料核验。</section>
      <Modal open={confirm} onOpenChange={setConfirm} title="开始一组新练习？" description="当前练习位置与未提交选择将被替换。已提交的答题记录、收藏和笔记会保留。">
        <div className="modal-actions"><Button variant="secondary" onClick={()=>{setConfirm(false);setConflictSession(null);if(conflictSession){void reloadSession(conflictSession.id);setShowSetup(false);}}}>继续保留</Button><Button onClick={()=>void start(true)}>开始新练习</Button></div>
      </Modal>
    </div>;
  }

  const answered=Object.values(session.answers).filter(item=>item.submitted).length;
  const correct=Object.values(session.answers).filter(item=>item.correct===true).length;
  if(session.finished&&!isExam){
    return <div className="page-enter"><PageTitle eyebrow="SESSION COMPLETE" title="又向前迈了一步。" description="练习已经保存。回顾错题，把这次练习变成下一次的进步。"/><div className="panel result-panel"><div className="result-icon"><CheckCircle2 size={42}/></div><h2>{isExam?'本次考试已交卷':'本次练习已结束'}</h2><div className="result-stats"><div><strong>{session.score!=null?`${session.score}${session.maxScore!=null?`/${session.maxScore}`:''}`:`${answered}/${session.questionIds.length}`}</strong><span>{isExam?'服务器评分':'已提交'}</span></div><div><strong>{percent(correct,Math.max(1,answered))}%</strong><span>本次正确率（含辅助）</span></div><div><strong>{fmtTime(session.elapsedSeconds)}</strong><span>学习用时</span></div></div><div className="button-row"><Button onClick={()=>update({...session,finished:false,status:'active'})}>回顾本次题目</Button><Button variant="secondary" onClick={()=>setShowSetup(true)}>开启新练习 <ArrowRight size={16}/></Button></div></div></div>;
  }

  const answerSync=answer.sync||(answer.pending?'pending':'synced');
  const showResultPanel=answer.submitted&&(revealed||isExam&&examSubmitted);
  const optionState=(key:Option)=>{
    if(!answer.submitted) return answer.selected===key?'chosen':'';
    if(showResultPanel){
      const correctAnswer=solution?.answer||examResults[q.id]?.answer;
      if(correctAnswer) return key===correctAnswer?'correct':answer.selected===key?'incorrect':'muted';
      if(typeof answer.correct==='boolean') return answer.correct&&answer.selected===key?'correct':!answer.correct&&answer.selected===key?'incorrect':'muted';
    }
    return answer.selected===key?'chosen':'muted';
  };
  return <div className="page-enter">
    <div className="practice-header"><button className="text-link" onClick={()=>setShowSetup(true)}><ArrowLeft size={16}/> 练习设置</button><div><SyncStatus compact/><span><Clock3 size={15}/>{isExam&&remaining!=null?(remaining>0?`剩余 ${fmtTime(remaining)}`:'已到截止时间'):fmtTime(session.elapsedSeconds)}</span></div></div>
    {pageError&&<div className="inline-error" role="alert">{pageError}<button onClick={()=>setPageError('')}>关闭</button></div>}
    {session.scope.materialId&&<div className="active-chapter-scope"><span>章节练习 · {catalog.materials.find(material=>material.id===session.scope.materialId)?.title||'已保存的章节'}</span><Link to="/knowledge" search={{id:session.scope.materialId,section:session.scope.sectionId,from:'practice'}} className="text-link">返回本章原文 <ArrowRight size={14}/></Link></div>}
    {isExam&&examSubmitted&&<div className="panel exam-result"><div><span className="eyebrow">EXAM SUBMITTED</span><h2>试卷已交卷</h2><p>答案与解析已由服务器确认；逐题结果可在答题卡与题目中查看。</p></div><div className="exam-score"><strong>{session.score??'—'}<small>{session.maxScore!=null?` / ${session.maxScore}`:''}</small></strong><span>服务器评分</span></div></div>}
    {isExam&&!examSubmitted&&<div className="exam-banner"><Clock3 size={16}/><span>正式考试：交卷前不公布答案。截止时间以服务器时间为准，断线不会延长。</span><Button variant="secondary" onClick={()=>setConfirm(true)}>提前交卷</Button></div>}
    <div className="practice-layout"><main className="question-column">
      <div className="question-topline"><div><Badge tone="green">{modules[q.module]||'综合知识'}</Badge><Badge>{sourceTypeLabel(q.sourceType)}</Badge>{isExam&&<Badge tone="amber">正式考试</Badge>}</div><span>第 <strong>{session.index+1}</strong> / {session.questionIds.length} 题</span></div>
      <div className="progress-track practice-progress"><i style={{width:`${percent(answered,session.questionIds.length)}%`}}/></div>
      <article className="panel question-card" key={q.id}><div className="question-source">{q.term} · 原题第 {q.questionNo} 题 {q.multiBlank&&<Badge tone="amber">请回答第 {q.questionNo} 空</Badge>}</div><div className="question-stem"><Markdown>{q.stem}</Markdown></div>{q.figureMissing&&<div className="inline-warning">本题疑似引用缺失图示。请核验原始资料，不要依赖 AI 推测图中条件。</div>}{q.figure?.kind==='layer-stack'&&<div className="layer-figure">{q.figure.layers?.map(layer=><div key={layer}>{layer}</div>)}<small>{q.figure.caption}</small></div>}{q.figure?.code&&<><Markdown>{`\`\`\`mermaid\n${q.figure.code}\n\`\`\``}</Markdown><small>{q.figure.caption}</small></>}
        <div className="option-list" role="radiogroup" aria-label="选择答案">{(['A','B','C','D'] as Option[]).map(key=><button key={key} role="radio" aria-checked={answer.selected===key} disabled={!!answer.submitted||saving} className={`option ${optionState(key)}`} onClick={()=>choose(key)}><span className="option-key">{key}</span><span>{q.options[key]}</span>{showResultPanel&&(solution?.answer||examResults[q.id]?.answer)===key?<CheckCircle2 size={19}/>:showResultPanel&&answer.selected===key?<XCircle size={19}/>:null}</button>)}</div>
        <div className="question-tools"><button className={mark?.favorite?'marked':''} onClick={()=>markQuestion({favorite:!mark?.favorite})}><Bookmark size={16} fill={mark?.favorite?'currentColor':'none'}/>{mark?.favorite?'已收藏':'收藏题目'}</button><button className={mark?.uncertain?'marked':''} onClick={()=>markQuestion({uncertain:!mark?.uncertain})}><Flag size={16}/>{mark?.uncertain?'已标记存疑':'标记存疑'}</button><button onClick={()=>ai(!answer.submitted)}><Lightbulb size={16}/>{answer.submitted?'问问 AI':'给我一点提示'}</button></div>
      </article>
      {markConflict&&<div className="inline-error mark-conflict" role="alert"><span>{markConflict.message}</span><button onClick={()=>{if(markConflict.serverValues){setMarks(rows=>rows.map(item=>item.id===markConflict.questionId?{...item,...markConflict.serverValues} as Mark:item));}setMarkConflict(null);}}>使用服务器版本</button><button onClick={()=>{const current=marks.find(item=>item.id===markConflict.questionId);if(current)void updateMark({...current,version:markConflict.serverVersion});setMarkConflict(null);}}>保留我的更改</button></div>}
      {answer.submitted&&<section className={`panel answer-panel ${showResultPanel?(correctNow===false?'failure':'success'):'pending'}`}>
        <div className="answer-heading">{showResultPanel?(correctNow===false?<XCircle size={23}/>:<CheckCircle2 size={23}/>):<CloudOff size={23}/>}<div><h3>{showResultPanel?(correctNow===false?'找到一个值得巩固的知识点。':'回答正确，继续保持。'):answerSync==='pending'?'已保存到本机，等待同步。':'答案已记录，交卷后公布结果。'}</h3><p>你的答案 {answer.selected}{showResultPanel&&solution?.answer?` · 参考答案 ${solution.answer}`:''}{answer.assisted?' · 本题使用过 AI 辅助':''}</p></div></div>
        {showResultPanel?<><h4>第三方参考解析</h4>{solution?.analysis?<Markdown>{solution.analysis}</Markdown>:q.analysis&&q.analysis.length>20?<Markdown>{q.analysis}</Markdown>:<p className="muted-text">原始资料未提供详细解析。你可以查阅相关知识，或请 AI 辅助解释。</p>}<SourceLink href={q.sourceUrl}/><QuestionKnowledgeLinks question={q} wrong={correctNow===false}/><div className="answer-ai"><Sparkles size={19}/><span>想弄清每个选项背后的区别？</span><Button variant="secondary" onClick={()=>ai(false)}>AI 辅导 <ArrowRight size={14}/></Button></div></>:<p className="muted-text">{isExam?'正式考试交卷前不会显示参考答案，避免影响后续题目。':'等待服务器确认作答结果。'}</p>}
      </section>}
      <details className="panel question-notes"><summary>我的笔记 <span>记录这道题带来的启发</span></summary><textarea aria-label="本题笔记" placeholder="我的易错点、解题方法…" value={mark?.note||''} onChange={event=>changeNote(event.target.value)}/></details>
      <div className="question-navigation"><Button variant="secondary" disabled={session.index===0||saving} onClick={()=>move(-1)}><ChevronLeft size={17}/> 上一题</Button><span className="keyboard-hint">A–D 选择 · Enter 提交 · ← → 切题</span>{!answer.submitted?<Button disabled={!answer.selected||saving} onClick={()=>void submit()}>{saving?'正在保存…':'提交答案'}<Check size={17}/></Button>:session.index===session.questionIds.length-1?<Button onClick={()=>isExam?setConfirm(true):void finish()}>{isExam?'交卷':'完成练习'} <Check size={17}/></Button>:<Button onClick={()=>move(1)}>下一题 <ChevronRight size={17}/></Button>}</div>
    </main>
    <aside className="practice-aside"><section className="panel answer-sheet"><div className="section-heading"><h3><ListChecks size={17}/> 答题卡</h3><small>{answered}/{session.questionIds.length}</small></div><div className="question-grid">{session.questionIds.map((id,index)=>{const state=session.answers[id],result=examResults[id];const original=catalog.choices.find(question=>question.id===id);const correctAnswer=solution?.answer&&id===q.id?solution.answer:original?.answer;return <button aria-label={`跳转第 ${index+1} 题`} key={id} disabled={saving} className={`${index===session.index?'current':''} ${state?.submitted?(examSubmitted&&result?(result.correct?'right':'wrong'):state.correct===true?'right':state.correct===false?'wrong':'draft'):state?.selected?'draft':''}`} onClick={()=>update({...session,index})}>{index+1}</button>;})}</div><div className="sheet-legend"><span><i className="right"/>正确</span><span><i className="wrong"/>错误</span><span><i className="draft"/>已选</span></div>{isExam&&!examSubmitted?<Button variant="secondary" onClick={()=>setConfirm(true)}>交卷</Button>:<Button variant="ghost" onClick={()=>void finish()}>结束本次练习</Button>}</section><section className="ai-nudge"><Sparkles size={23}/><h3>理解，比记住答案更重要。</h3><p>一点提示、一个例子，或一次追问。学习搭子陪你理清思路。</p><Button variant="secondary" onClick={()=>ai(!answer.submitted)}>打开 AI 辅导 <ArrowRight size={15}/></Button></section>{answer.submitted?<QuestionKnowledgeLinks question={q} compact/>:<section className="related-lessons"><h4>本题关联原文</h4><p className="unmapped-note">提交答案后，可回到对应知识点复习。</p></section>}</aside></div>
    <Modal open={confirm} onOpenChange={setConfirm} title={isExam?'确认交卷？':'开始一组新练习？'} description={isExam?'交卷后立即由服务器评分，之后不能修改答案。':'当前练习位置与未提交选择将被替换。已提交的答题记录、收藏和笔记会保留。'}>
      <div className="modal-actions">{isExam?<><Button variant="secondary" onClick={()=>setConfirm(false)}>继续答题</Button><Button onClick={()=>void submitExam()}>确认交卷</Button></>:<><Button variant="secondary" onClick={()=>{setConfirm(false);setConflictSession(null);if(conflictSession){void reloadSession(conflictSession.id);setShowSetup(false);}}}>继续保留</Button><Button onClick={()=>void start(true)}>开始新练习</Button></>}</div>
    </Modal>
  </div>;
}
