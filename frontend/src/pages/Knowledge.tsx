import {useEffect,useRef,useState} from 'react';
import {Link,useLocation} from '@tanstack/react-router';
import {ArrowLeft,ArrowRight,BookOpen,Check,CheckCircle2,Download,FileText,List,MessageSquarePlus,Search,Sparkles,Trash2} from 'lucide-react';
import {useApp} from '../context';
import {apiDelete,apiGet,apiPost,errorText,normalizeAnnotationList,normalizeMaterial} from '../api';
import {cacheGet,cachePut,notifySync} from '../sync';
import {enqueue} from '../outbox';
import {db} from '../db';
import {restoreReading} from '../reading';
import {KnowledgeIndexes} from '../components/KnowledgeIndexes';
import {ChapterExercises,ChapterPracticeButton} from '../components/LearningLinks';
import {belongsToMaterial} from '../study';
import type {Material,MaterialSection,Reading} from '../types';
import {Badge,Button,Empty,Loading,Markdown,Modal,PageTitle,SourceLink} from '../ui';

const labels={textbook:'教材原文',outline:'考试大纲',notes:'专题与速记',guide:'备考方法'};
const bookIntro={textbook:'按教材章节系统阅读，保留源资料中的概念、论述、例题、表格和结构化图示。',outline:'查看完整的大纲与考试说明，文件内容沿用来源版本。',notes:'完整的考点手册和专题笔记，适合复习时对照查阅。',guide:'案例答题方法、论文主题与写作资料，按源文件完整阅读。'};
const normalizeTitle=(value:string)=>value.replace(/[*`📚📕📘📗📙⭐🔥🚀]/gu,'').trim();
const readerInset=()=>((document.querySelector('.reader-tools')?.getBoundingClientRect().height)||60)+24;
const lengthLabel=(chars:number)=>chars>=10000?`约 ${(chars/10000).toFixed(1)} 万字`:`约 ${Math.round(chars/100)*100} 字`;

type SearchResult={type:string;id:string;title:string;snippet:string;materialId?:string;sectionId?:string};

function normalizeSearch(raw:unknown):SearchResult[]{
  const wrapper=raw&&typeof raw==='object'?raw as Record<string,unknown>:{};
  const items=Array.isArray(wrapper.items)?wrapper.items:(Array.isArray(wrapper.results)?wrapper.results:[]);
  return items.filter((item):item is Record<string,unknown>=>!!item&&typeof item==='object').map(item=>{
    const materialId=typeof item.materialId==='string'?item.materialId:undefined;
    return {type:typeof item.type==='string'?item.type:(materialId?'material':'question'),id:String(item.id||item.questionId||materialId||''),title:String(item.title||item.sectionTitle||''),snippet:String(item.snippet||item.excerpt||item.text||''),materialId,sectionId:typeof item.sectionId==='string'?item.sectionId:undefined};
  }).filter(item=>!!item.id);
}

export function Knowledge(){
  const {catalog}=useApp();
  const {id,section:requestedSection,from,version}=useLocation().search as {id?:string;section?:string;from?:string;version?:string};
  const legacy=catalog.lessons.find(lesson=>lesson.id===id);
  const material=(catalog.materials||[]).find(item=>item.id===id||(legacy&&item.sourceFile===legacy.sourceFile));
  if(material){
    const section=version?requestedSection:material.sections.some(entry=>entry.id===requestedSection)?requestedSection:legacy?material.sections.find(entry=>normalizeTitle(entry.title)===normalizeTitle(legacy.title))?.id:undefined;
    return <MaterialReader key={`${material.id}:${id}:${section||''}:${version||''}`} version={version} material={material} initialSection={section} fromPractice={from==='practice'}/>;
  }
  return <MaterialLibrary/>;
}

function MaterialLibrary(){
  useEffect(()=>{window.scrollTo({top:0,behavior:'instant'});},[]);
  const {catalog,reading}=useApp();
  const materials=catalog.materials||[];
  const [kind,setKind]=useState<Material['kind']>('textbook');
  const [query,setQuery]=useState(''),[results,setResults]=useState<SearchResult[]>([]),[searching,setSearching]=useState(false),[error,setError]=useState('');
  useEffect(()=>{
    const trimmed=query.trim();
    if(!trimmed){setResults([]);setSearching(false);return;}
    const controller=new AbortController();setSearching(true);setError('');
    const timer=window.setTimeout(()=>{
      apiGet<unknown>('/api/content/search',{q:trimmed,limit:30},controller.signal).then(raw=>{if(!controller.signal.aborted)setResults(normalizeSearch(raw));}).catch(caught=>{if(!controller.signal.aborted)setError(`全文检索暂不可用：${errorText(caught)}`);}).finally(()=>{if(!controller.signal.aborted)setSearching(false);});
    },300);
    return()=>{controller.abort();window.clearTimeout(timer);};
  },[query]);
  const searchMaterialIds=new Set(results.filter(result=>result.materialId).map(result=>result.materialId));
  const filtered=materials.filter(material=>material.kind===kind&&(searchMaterialIds.has(material.id)||`${material.title} ${material.excerpt}`.toLowerCase().includes(query.toLowerCase())));
  const last=reading.slice().sort((a,b)=>b.updatedAt.localeCompare(a.updatedAt)).map(entry=>({reading:entry,material:materials.find(item=>item.id===entry.id)})).find(item=>item.material);
  const done=materials.filter(material=>reading.some(entry=>entry.id===material.id&&entry.done)).length;
  const textbooks=materials.filter(material=>material.kind==='textbook');
  return <div className="page-enter material-library">
    <PageTitle eyebrow="THE READING ROOM" title="从完整资料开始，系统地学。" description="每块内容对应一份完整的原始资料。打开章节，连续阅读详细原文。"/>
    <section className="material-intro"><span className="material-intro-icon"><BookOpen size={30}/></span><div><h2>系统架构设计师 · 完整教材资料</h2><p>{textbooks.length} 章教材与前言 · {lengthLabel(textbooks.reduce((sum,material)=>sum+material.chars,0))} · 章节目录定位</p><small>正文与版本由服务器提供；阅读位置会作为学习证据上报。</small></div>{last?.material&&<Button asChild variant="secondary"><Link to="/knowledge" search={{id:last.material.id}}>继续上次阅读 <ArrowRight size={15}/></Link></Button>}</section>
    <div className="material-library-toolbar"><div className="material-tabs" role="tablist" aria-label="资料类型">{Object.entries(labels).map(([key,label])=><button role="tab" aria-selected={kind===key} className={kind===key?'active':''} key={key} onClick={()=>setKind(key as Material['kind'])}>{label}<span>{materials.filter(material=>material.kind===key).length}</span></button>)}</div><div className="search-field"><Search size={16}/><input aria-label="搜索知识全文" placeholder="搜索资料标题与正文…" value={query} onChange={event=>setQuery(event.target.value)}/></div></div>
    <div className="material-library-caption"><p>{bookIntro[kind]}</p><span className="knowledge-count">{filtered.length} 份资料 · {done} 份已学</span></div>
    {error&&<div className="inline-error" role="alert">{error}</div>}
    {searching&&<div className="searching-line"><Loading/></div>}
    {query.trim()&&results.length>0&&<section className="panel search-results" aria-label="全文检索结果"><div className="section-heading"><h3>全文检索结果</h3><small>{results.length} 条</small></div>{results.slice(0,10).map((result,index)=>{const target=result.materialId&&materials.find(material=>material.id===result.materialId);return target?<Link key={`${result.type}:${result.id}:${result.sectionId||index}`} className="search-result-row" to="/knowledge" search={{id:target.id,...(result.sectionId?{section:result.sectionId}:{})}}><Badge>{result.type==='question'?'题目':'资料'}</Badge><div><strong>{result.title||target.title}</strong><span>{result.snippet||target.excerpt}</span></div><ArrowRight size={15}/></Link>:<div className="search-result-row" key={`${result.type}:${result.id}:${result.sectionId||index}`}><Badge>{result.type==='question'?'题目':'资料'}</Badge><div><strong>{result.title}</strong><span>{result.snippet}</span></div></div>;})}</section>}
    <div className="material-grid">{filtered.map(material=>{
      const status=reading.find(entry=>entry.id===material.id),chapter=material.title.match(/^第(\d+)章/);
      return <Link to="/knowledge" search={{id:material.id}} className="panel material-card" key={material.id}>
        <div className="material-card-top"><span className="material-number">{chapter?chapter[1].padStart(2,'0'):material.title==='前言'?'序':'文'}</span><Badge tone={kind==='textbook'?'green':'neutral'}>{labels[kind]} · 全文</Badge></div>
        <h3>{material.title.replace(/^第\d+章\s*/,'')}</h3><p>{material.excerpt||'从源文件完整收录，保留原有结构与内容。'}</p>
        <div className="material-card-meta"><span>{lengthLabel(material.chars)}</span><span>{material.sections.filter(section=>section.level===2).length} 个主要小节</span><span>{catalog.choices.filter(question=>!question.figureMissing&&belongsToMaterial(catalog.knowledgeLinks||{},question.id,material.id)).length} 道关联题</span></div>
        <div className="material-card-footer"><span>{status?.done?<><CheckCircle2 size={14}/> 已标记学完</>:status?<><BookOpen size={14}/> 继续阅读</>:<><FileText size={14}/> 阅读完整原文</>}</span><ArrowRight size={16}/></div>
      </Link>;
    })}</div>
    {!filtered.length&&!searching&&<Empty title="没有匹配的资料">试试其他关键词，或切换资料类型。</Empty>}
    <div className="material-library-note">资料正文由服务器按版本提供；历史引用仍可定位到对应版本的小节。</div>
  </div>;
}

function MaterialReader({material:baseMaterial,initialSection,fromPractice,version}:{material:Material;initialSection?:string;fromPractice?:boolean;version?:string}){
  const {catalog,reading,setAI}=useApp();
  const [material,setMaterial]=useState<Material>(baseMaterial);
  const [readingReady,setReadingReady]=useState(false);
  const [body,setBody]=useState(''),[error,setError]=useState(''),[retry,setRetry]=useState(0),[activeId,setActiveId]=useState(''),[tocOpen,setTocOpen]=useState(false);
  const [annotations,setAnnotations]=useState<Record<string,unknown>[]>([]),[annotationDraft,setAnnotationDraft]=useState<{quote:string;sectionId:string}|null>(null),[annotationNote,setAnnotationNote]=useState(''),[annotationError,setAnnotationError]=useState('');
  const article=useRef<HTMLDivElement>(null);
  const current=reading.find(entry=>entry.id===material.id);
  const path=`/api/content/materials/${encodeURIComponent(material.id)}/raw?version=${encodeURIComponent(material.versionId||version||'')}`;
  const sections=(material.sections||[]).filter(section=>section.level===2||section.level===3);

  useEffect(()=>{
    let alive=true;const controller=new AbortController();setBody('');setError('');setAI(null);
    apiGet<Record<string,unknown>>(`/api/content/materials/${encodeURIComponent(baseMaterial.id)}/content`,{version},controller.signal).then(raw=>{
      if(!alive)return;
      setMaterial(normalizeMaterial({...baseMaterial,...raw,id:baseMaterial.id,title:baseMaterial.title},0));
      setBody(String(raw.content||''));
    }).catch(caught=>{if(alive&&!controller.signal.aborted)setError(errorText(caught));});
    return()=>{alive=false;controller.abort();};
  },[baseMaterial.id,version,retry,setAI]);

  useEffect(()=>{
    apiGet<unknown>('/api/content/annotations',{materialId:material.id}).then(raw=>setAnnotations(normalizeAnnotationList(raw))).catch(()=>{/* annotations are optional until the endpoint is ready */});
  },[material.id]);

  useEffect(()=>{
    if(!body)return;
    setReadingReady(false);
    let cancelled=false,ready=false,timer:ReturnType<typeof setTimeout>,frame=0,lastPosition:Omit<Reading,'done'>|null=null,lastReport=0,lastScrollY=window.scrollY;
    const report=(position:Omit<Reading,'done'>)=>{
      const section=sections.find(entry=>entry.id===position.sectionId);
      const payload={materialId:material.id,materialVersionId:material.versionId||material.currentVersionId||'',progress:position.progress,sectionId:position.sectionId||'',sectionKey:section?.key||'',offset:Math.round(position.sectionOffset||0)};
      void enqueue({dedupeKey:`reading:${material.id}`,kind:'reading',method:'POST',path:'/api/learning/reading',body:payload});
    };
    const save=(force=false)=>{if(!lastPosition)return;const position=lastPosition;void db.transaction('rw',db.reading,async()=>{const previous=await db.reading.get(material.id);await db.reading.put({...position,done:previous?.done||false});}).then(()=>cachePut(`reading:${material.id}`,position)).catch(()=>{});if(force||Date.now()-lastReport>4000){lastReport=Date.now();report(position);}};
    const measure=()=>{
      if(!ready||cancelled)return;
      const headings=[...article.current?.querySelectorAll<HTMLElement>('[data-section-heading]')||[]];
      const heading=headings.filter(element=>element.getBoundingClientRect().top<=readerInset()+10).at(-1)||headings[0];
      if(heading)setActiveId(heading.id);
      const maxScroll=document.documentElement.scrollHeight-window.innerHeight;
      lastPosition={id:material.id,progress:maxScroll>0?Math.max(0,Math.min(1,window.scrollY/maxScroll)):0,updatedAt:new Date().toISOString(),...(heading?{sectionId:heading.id,sectionOffset:Math.round(Math.max(0,window.scrollY-(heading.getBoundingClientRect().top+window.scrollY)))}:{})};
      clearTimeout(timer);timer=setTimeout(save,350);
    };
    const onScroll=()=>{if(!ready||cancelled||window.scrollY===lastScrollY)return;lastScrollY=window.scrollY;cancelAnimationFrame(frame);frame=requestAnimationFrame(measure);};
    restoreReading(material.id).then(saved=>{
      if(cancelled)return;
      frame=requestAnimationFrame(()=>{
        const target=initialSection||saved?.sectionId;
        const heading=target?document.getElementById(target):null;
        const top=heading?heading.getBoundingClientRect().top+window.scrollY+(initialSection?-readerInset():saved?.sectionOffset||0):(saved?.progress||0)*Math.max(0,document.documentElement.scrollHeight-window.innerHeight);
        window.scrollTo({top:Math.max(0,top),behavior:'instant'});lastScrollY=window.scrollY;
        if(heading)setActiveId(heading.id);
        frame=requestAnimationFrame(()=>{ready=true;setReadingReady(true);});
      });
    }).catch(()=>{});
    const leave=()=>save(true);
    const visibility=()=>{if(document.visibilityState==='hidden')save(true);};
    window.addEventListener('scroll',onScroll,{passive:true});
    window.addEventListener('pagehide',leave);document.addEventListener('visibilitychange',visibility);
    return()=>{cancelled=true;clearTimeout(timer);cancelAnimationFrame(frame);window.removeEventListener('scroll',onScroll);window.removeEventListener('pagehide',leave);document.removeEventListener('visibilitychange',visibility);save(true);};
  },[body,material.id,material.versionId,initialSection]);

  function jump(section:MaterialSection){
    const element=document.getElementById(section.id);
    if(element){setTocOpen(false);window.scrollTo({top:Math.max(0,element.getBoundingClientRect().top+window.scrollY-readerInset()),behavior:'instant'});setActiveId(section.id);}
  }
  const activeSection=material.sections.find(section=>section.id===activeId)||material.sections[0];
  const activeTocId=sections.filter(section=>section.start<=(activeSection?.start||0)).at(-1)?.id;
  function ask(){
    const selection=window.getSelection();
    const selected=selection&&article.current?.contains(selection.anchorNode)?selection.toString().trim():'';
    const section=activeSection;
    const next=section?material.sections.find(entry=>entry.start>section.start&&entry.level<=section.level):undefined;
    const excerpt=section?body.slice(section.start,next?.start).slice(0,20000):body.slice(0,20000);
    setAI({id:`material:${material.id}:${section?.id||'intro'}`,title:`${material.title}${section?` · ${section.title}`:''}`,kind:'lesson',module:material.module,materialId:material.id,materialVersionId:material.versionId,sectionId:section?.id,text:`来源：${material.sourceUrl}\n${selected?`我选中的原文：${selected.slice(0,8000)}\n`:''}当前阅读小节的原文：\n${excerpt}`});
  }
  function beginAnnotation(){
    const selection=window.getSelection();
    const quote=selection&&article.current?.contains(selection.anchorNode)?selection.toString().trim():'';
    if(!quote){setAnnotationError('请先在正文中选中要保存的文字。');return;}
    setAnnotationError('');setAnnotationNote('');setAnnotationDraft({quote:quote.slice(0,4000),sectionId:activeSection?.id||''});
  }
  async function saveAnnotation(){
    if(!annotationDraft)return;
    const payload={materialId:material.id,sectionId:annotationDraft.sectionId,quote:annotationDraft.quote,note:annotationNote.trim()};
    try{
      await apiPost('/api/content/annotations',payload);
      const raw=await apiGet<unknown>('/api/content/annotations',{materialId:material.id});
      setAnnotations(normalizeAnnotationList(raw));setAnnotationDraft(null);setAnnotationNote('');
    }catch(caught){setAnnotationError(errorText(caught));}
  }
  async function deleteAnnotation(id:string){
    try{await apiDelete(`/api/content/annotations/${encodeURIComponent(id)}`);setAnnotations(rows=>rows.filter(row=>String(row.id)!==id));notifySync();}catch(caught){setAnnotationError(errorText(caught));}
  }
  async function toggleDone(){
    try{
      await db.transaction('rw',db.reading,async()=>{const saved=await db.reading.get(material.id);await db.reading.put({id:material.id,progress:0,...saved,done:!saved?.done,updatedAt:new Date().toISOString()});});
      const nextDone=!current?.done;
      await enqueue({dedupeKey:`reading:${material.id}`,kind:'reading',method:'POST',path:'/api/learning/reading',body:{materialId:material.id,materialVersionId:material.versionId||material.currentVersionId||'',progress:1,sectionId:activeSection?.id||'',sectionKey:activeSection?.key||'',offset:0,done:nextDone}});
    }catch(caught){setError(errorText(caught));}
  }
  const siblings=(catalog.materials||[]).filter(item=>item.kind===material.kind),index=siblings.findIndex(item=>item.id===material.id);
  return <div className="page-enter material-reader">
    <Link to="/knowledge" search={{}} className="text-link back-link"><ArrowLeft size={15}/> 返回完整资料目录</Link>
    <PageTitle eyebrow="READ THE ORIGINAL" title={material.title} description={`${labels[material.kind]} · 完整源资料 · ${lengthLabel(material.chars)}`}/>
    {fromPractice&&<div className="reading-return-banner"><span><BookOpen size={16}/>正在回看本题关联原文，答题现场已保留。</span><Button asChild variant="secondary"><Link to="/practice" search={{}}>返回本题继续练习 <ArrowRight size={15}/></Link></Button></div>}
    <div className="reader-tools"><div className="button-row">{fromPractice&&<Button asChild variant="secondary"><Link to="/practice" search={{}}>返回本题 <ArrowRight size={14}/></Link></Button>}<ChapterPracticeButton material={material}/><Button variant={current?.done?'primary':'secondary'} onClick={()=>void toggleDone()}><Check size={15}/>{current?.done?'已标记学完':'标记为已学'}</Button><Button variant="secondary" disabled={!body} onMouseDown={event=>event.preventDefault()} onClick={ask}><Sparkles size={15}/> AI 讲解 / 解释选中文本</Button><Button variant="secondary" disabled={!body} onClick={beginAnnotation}><MessageSquarePlus size={15}/> 添加批注</Button></div><a className="text-link" href={path} download={`${material.title}.md`}><Download size={15}/> 下载原始资料</a><Button variant="secondary" className="mobile-toc-toggle" aria-expanded={tocOpen} aria-controls="material-toc" onClick={()=>setTocOpen(!tocOpen)}><List size={16}/> 章内目录</Button></div>
    {annotationError&&<div className="inline-error" role="alert">{annotationError}<button onClick={()=>setAnnotationError('')}>关闭</button></div>}
    <KnowledgeIndexes materialId={material.id}/><div className="reader-layout"><article className="panel reading-article source-document" ref={article}>
      <div className="original-source-note"><BookOpen size={17}/><div><strong>{material.kind==='textbook'?'教材清洗版 · 完整原文':'原始资料 · 全文保留'}</strong><p>保留源文件的段落、例子、表格和图示说明。下方内容未经 AI 摘要或改写。{material.version!=null?` 当前版本 v${material.version}。`:''}</p><SourceLink href={material.sourceUrl}>查看上游源文件</SourceLink></div></div>
      {error?<div className="inline-error" role="alert">{error}<Button variant="secondary" onClick={()=>setRetry(retry+1)}>重新加载</Button></div>:!body?<Loading/>:<Markdown anchors sourceUrl={material.sourceUrl}>{body}</Markdown>}
      <footer className="reading-source"><p>已到本份源资料末尾。保留来源版本的内容与声明，原文中的缺图或待补项仍按原样展示。</p><SourceLink href={material.sourceUrl}/><span>{material.license}</span></footer>
      <ChapterExercises material={material} sectionId={activeTocId}/>
      <div className="document-pagination">{siblings[index-1]?<Link to="/knowledge" search={{id:siblings[index-1].id}}><ArrowLeft size={15}/><span>上一篇<small>{siblings[index-1].title}</small></span></Link>:<span/>}{siblings[index+1]&&<Link to="/knowledge" search={{id:siblings[index+1].id}}><span>下一篇<small>{siblings[index+1].title}</small></span><ArrowRight size={15}/></Link>}</div>
    </article><aside id="material-toc" className={`panel material-toc ${tocOpen?'toc-open':''}`}><div className="section-heading"><h3><List size={16}/> 章内目录</h3><small>{sections.length} 节</small></div><div className="toc-scroll">{sections.map(section=><button key={section.id} className={`${activeTocId===section.id?'active':''} level-${section.level}`} onClick={()=>jump(section)}>{section.title}</button>)}{!sections.length&&<p>本资料没有单独的小节标题，可直接阅读全文。</p>}</div><div className="toc-footer"><span className="save-indicator"><i/>{readingReady?'阅读位置自动保存':'正在恢复阅读位置…'}</span><button onClick={()=>window.scrollTo({top:0,behavior:'instant'})}>回到开头 ↑</button></div></aside></div>
    <section className="panel annotations-panel" aria-label="我的批注"><div className="section-heading"><h3>我的批注</h3><small>{annotations.length} 条</small></div>{annotations.length?<div className="annotation-list">{annotations.map(annotation=>{const id=String(annotation.id||'');const quote=String(annotation.quote||annotation.content||annotation.text||'');const note=String(annotation.note||'');return <article className="annotation-item" key={id}><blockquote>{quote}</blockquote>{note&&<p>{note}</p>}<button aria-label="删除批注" onClick={()=>void deleteAnnotation(id)}><Trash2 size={14}/>删除</button></article>;})}</div>:<p className="muted-text">在正文中选中文字后，点击「添加批注」即可保存摘录和笔记。</p>}</section>
    <Modal open={!!annotationDraft} onOpenChange={value=>{if(!value)setAnnotationDraft(null);}} title="保存批注" description="摘录会绑定当前资料与版本，方便以后回查。"><div className="annotation-quote">{annotationDraft?.quote}</div><label>我的笔记<textarea aria-label="批注笔记" rows={4} value={annotationNote} onChange={event=>setAnnotationNote(event.target.value)} placeholder="这段内容对我的启发…"/></label>{annotationError&&<div className="inline-error" role="alert">{annotationError}</div>}<div className="modal-actions"><Button variant="secondary" onClick={()=>setAnnotationDraft(null)}>取消</Button><Button onClick={()=>void saveAnnotation()}>保存批注</Button></div></Modal>
  </div>;
}
