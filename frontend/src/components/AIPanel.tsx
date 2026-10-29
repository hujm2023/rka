import {useEffect,useRef,useState} from 'react';
import {Link} from '@tanstack/react-router';
import * as Dialog from '@radix-ui/react-dialog';
import {ArrowUp,Square,Sparkles,X,BookOpen,Settings2,RotateCcw,AlertTriangle} from 'lucide-react';
import {useApp} from '../context';
import {db} from '../db';
import {askAI,buildMessages,consumeAIResponse} from '../ai';
import {apiGet,apiPost,errorText,streamSse} from '../api';
import {newId} from '../id';
import {Markdown,Button,Badge} from '../ui';
import type {ChatMessage,Citation} from '../types';

type TaskEvent={seq?:number;type?:string;payload?:Record<string,unknown>};
type AIStatus={configured?:boolean;unavailableReason?:string;connectionStatus?:string;capabilityStatus?:string;teaching?:{configured?:boolean;reason?:string}};

const isServerEndpoint=(endpoint:string)=>!endpoint.trim()||endpoint.trim()==='/api/ai';
const sleep=(ms:number)=>new Promise(resolve=>setTimeout(resolve,ms));

export function AIPanel(){
  const {ai,setAI,catalog,settings,apiKey}=useApp();
  return <Dialog.Root open={!!ai} onOpenChange={open=>{if(!open)setAI(null);}}><Dialog.Portal><Dialog.Overlay className="ai-overlay"/><Dialog.Content className="ai-panel" aria-describedby="ai-description"><Dialog.Title className="ai-title"><span className="ai-avatar"><Sparkles size={20}/></span><div>学习搭子 <Badge tone="green">AI</Badge><small>一起把问题弄明白</small></div></Dialog.Title><Dialog.Close className="icon-button ai-close" aria-label="关闭 AI 辅导"><X size={20}/></Dialog.Close><Dialog.Description id="ai-description" className="ai-description">结合当前学习内容与知识资料回答。AI 建议仅供学习，引用可回查；任务在服务器上执行，可取消并断线续接。</Dialog.Description>{ai&&<Conversation key={ai.id} context={ai} catalog={catalog} settings={settings} apiKey={apiKey}/>}</Dialog.Content></Dialog.Portal></Dialog.Root>;
}

function Conversation({context,catalog,settings,apiKey}:{context:NonNullable<ReturnType<typeof useApp>['ai']>;catalog:ReturnType<typeof useApp>['catalog'];settings:ReturnType<typeof useApp>['settings'];apiKey:string}){
  const {setAI,notify}=useApp();
  const serverMode=isServerEndpoint(settings.endpoint);
  const [messages,setMessages]=useState<ChatMessage[]>([]),[input,setInput]=useState(''),[busy,setBusy]=useState(false),[stream,setStream]=useState(''),[error,setError]=useState(''),[loaded,setLoaded]=useState(false);
  const [status,setStatus]=useState<AIStatus|null>(null),[citations,setCitations]=useState<Citation[]>([]),[taskId,setTaskId]=useState(''),[lastSeq,setLastSeq]=useState(0),[waitingQuestion,setWaitingQuestion]=useState('');
  const controller=useRef<AbortController|null>(null),alive=useRef(true),scroll=useRef<HTMLDivElement>(null),busyRef=useRef(false),idempotency=useRef(''),streamTextRef=useRef(''),lastSeqRef=useRef(0),citationsRef=useRef<Citation[]>([]);
  useEffect(()=>{alive.current=true;db.chats.get(context.id).then(chat=>{if(alive.current){setMessages(chat?.messages||[]);setLoaded(true);}}).catch(()=>setLoaded(true));return()=>{alive.current=false;controller.current?.abort();};},[context.id]);
  useEffect(()=>{if(!serverMode)return;let active=true;apiGet<AIStatus>('/api/ai/status').then(value=>{if(active)setStatus(value);}).catch(()=>{if(active)setStatus(null);});return()=>{active=false;};},[serverMode]);
  useEffect(()=>{const element=scroll.current;if(element)element.scrollTop=element.scrollHeight;},[messages,stream,busy]);
  const prompts=context.kind==='question'?(context.hint?['给我一个提示，先别告诉我答案','这题考查哪个概念？']:['解释这道题的解题思路','逐项比较，为什么其他选项不合适？','用一个实际例子解释这个考点']):context.kind==='case'?['点评我的答案，指出遗漏的要点','怎样把我的回答写得更准确？']:context.kind==='essay'?['检查提纲是否回应了题目要求','逐段点评我的正文，定位需要修改的原句','检查项目背景与技术方案是否自洽']:['用通俗的例子解释这一节','列出易混概念并对比','问我一个问题，检查我是否理解了'];

  function appendAssistant(content:string,sources:Citation[]=[]){
    const assistant:ChatMessage={id:newId(),role:'assistant',content,createdAt:new Date().toISOString(),sources};
    setMessages(current=>{const next=[...current,assistant];void db.chats.put({id:context.id,messages:next,updatedAt:new Date().toISOString()});return next;});
    setStream('');streamTextRef.current='';
  }

  function taskKind(){
    if(context.kind==='question')return context.hint?'hint':'explain';
    if(context.kind==='case')return 'review_case';
    if(context.kind==='essay')return 'review_essay';
    return 'explain';
  }
  function taskContext(prompt:string){
    const value:Record<string,unknown>={prompt:prompt.slice(0,12000),hint:!!context.hint};
    if(context.questionId)value.questionId=context.questionId;
    if(context.questionRevision)value.questionRevision=context.questionRevision;
    if(context.sessionId)value.sessionId=context.sessionId;
    if(context.materialId)value.materialId=context.materialId;
    if(context.materialVersionId)value.materialVersionId=context.materialVersionId;
    if(context.sectionId)value.sectionId=context.sectionId;
    if(context.documentId)value.documentId=context.documentId;
    if(context.documentVersion!=null)value.documentVersion=context.documentVersion;
    return value;
  }

  async function readTaskStream(id:string,initialSeq:number,abort:AbortController){
    let seq=initialSeq,attempt=0,finished=false;
    while(!finished&&!abort.signal.aborted){
      try{
        await streamSse(`/api/ai/tasks/${encodeURIComponent(id)}/events?after=${seq}`,event=>{
          const data=(event.data||{}) as TaskEvent;
          if(typeof data.seq==='number'){seq=data.seq;lastSeqRef.current=data.seq;setLastSeq(data.seq);}
          const type=data.type||event.event;
          const payload=(data.payload||{}) as Record<string,unknown>;
          if(type==='delta'){
            const text=typeof payload.text==='string'?payload.text:'';
            if(text){streamTextRef.current+=text;if(streamTextRef.current.trim())context.onHint?.();setStream(streamTextRef.current);}
          }else if(type==='citation'){
            const citation:Citation={id:String(payload.sourceId||payload.id||`S${citationsRef.current.length}`),title:String(payload.title||'参考原文'),text:String(payload.text||''),materialId:typeof payload.materialId==='string'?payload.materialId:undefined,materialVersionId:typeof payload.materialVersionId==='string'?payload.materialVersionId:undefined,sectionId:typeof payload.sectionId==='string'?payload.sectionId:undefined};
            // Update the ref synchronously: a `done` event in the same stream
            // batch can arrive before React processes the state update.
            if(!citationsRef.current.some(item=>item.id===citation.id)) citationsRef.current=[...citationsRef.current,citation];
            setCitations(citationsRef.current);
          }else if(type==='question'){
            setWaitingQuestion(String(payload.text||payload.prompt||'AI 正在等待你的回答。'));
          }else if(type==='done'){
            finished=true;appendAssistant(streamTextRef.current.trim()||'（任务已完成，但没有返回文本。）',citationsRef.current);
            setWaitingQuestion('');
          }else if(type==='error'){
            finished=true;setError(String(payload.message||payload.reason||'AI 任务失败。'));
          }else if(type==='cancelled'){
            finished=true;appendAssistant(streamTextRef.current.trim()||'任务已取消。',citationsRef.current);
          }
        },abort.signal,seq?String(seq):undefined);
        if(finished) break;
        if(abort.signal.aborted) break;
      }catch(caught){
        if(abort.signal.aborted) break;
        if(++attempt>3){setError(`事件流中断，已保留已接收内容。${errorText(caught)}`);break;}
        await sleep(800*attempt);
      }
    }
  }

  async function sendServer(text:string,isFollowUp=false){
    const idKey=isFollowUp?newId():(()=>{if(!idempotency.current)idempotency.current=newId();return idempotency.current;})();
    const user:ChatMessage={id:newId(),role:'user',content:text,createdAt:new Date().toISOString()};
    setMessages(current=>{const next=[...current,user];void db.chats.put({id:context.id,messages:next,updatedAt:new Date().toISOString()});return next;});
    const abort=new AbortController();controller.current=abort;
    try{
      let activeTaskId=taskId;
      if(!isFollowUp||!activeTaskId){
        const created=await apiPost<{task?:{id?:string}}>('/api/ai/tasks',{kind:taskKind(),idempotencyKey:idKey,context:taskContext(text)});
        activeTaskId=created.task?.id||'';
        if(!activeTaskId)throw new Error('服务器未返回任务 ID。');
        setTaskId(activeTaskId);setLastSeq(0);lastSeqRef.current=0;streamTextRef.current='';setStream('');
      }else{
        await apiPost(`/api/ai/tasks/${encodeURIComponent(activeTaskId)}/messages`,{content:text});
      }
      await readTaskStream(activeTaskId,isFollowUp?lastSeqRef.current:0,abort);
    }catch(caught){
      if(!abort.signal.aborted)setError(errorText(caught));
    }finally{
      busyRef.current=false;if(alive.current)setBusy(false);
    }
  }

  async function send(text:string){
    if(!text.trim()||busyRef.current||!loaded||!serverMode&&!settings.endpoint.trim())return;
    busyRef.current=true;setBusy(true);setError('');setStream('');streamTextRef.current='';setCitations([]);citationsRef.current=[];setWaitingQuestion('');
    const user:ChatMessage={id:newId(),role:'user',content:text.trim(),createdAt:new Date().toISOString()};
    setInput('');
    if(serverMode){await sendServer(text.trim(),!!taskId);return;}
    const next=[...messages,user];setMessages(next);
    const abort=new AbortController();controller.current=abort;const timer=setTimeout(()=>abort.abort(),120000);
    try{
      await db.chats.put({id:context.id,messages:next,updatedAt:new Date().toISOString()});
      const {sources,messages:request}=await buildMessages(context,text,messages,catalog,abort.signal);
      let received=false;
      const content=await askAI(settings,apiKey,request,abort.signal,value=>{if(!received&&value.trim()){received=true;context.onHint?.();}if(alive.current)setStream(value);});
      const assistant:ChatMessage={id:newId(),role:'assistant',content,createdAt:new Date().toISOString(),sources};
      await db.chats.put({id:context.id,messages:[...next,assistant],updatedAt:new Date().toISOString()});
      if(alive.current){setMessages([...next,assistant]);setStream('');}
    }catch(caught){
      if(alive.current)setError(abort.signal.aborted?'生成已停止或超时。下方未完成内容未保存，可重新提问。':errorText(caught));
    }finally{clearTimeout(timer);busyRef.current=false;if(alive.current)setBusy(false);}
  }

  async function cancel(){
    if(serverMode&&taskId){try{await apiPost(`/api/ai/tasks/${encodeURIComponent(taskId)}/cancel`,{});}catch{/* aborting the stream is still effective locally */}}
    controller.current?.abort();
    setBusy(false);busyRef.current=false;
    if(streamTextRef.current.trim()){appendAssistant(`${streamTextRef.current}\n\n（已取消）`,citations);}
    notify('已请求取消当前 AI 任务。');
  }

  const unavailable=serverMode&&status&&status.configured===false?(status.unavailableReason||'服务器尚未配置模型网关。'):'';
  return <><div className="ai-context"><BookOpen size={15}/><span>{context.title}</span></div>
    {serverMode&&unavailable&&<div className="inline-warning ai-unavailable"><AlertTriangle size={15}/><span>服务器 AI 暂不可用：{unavailable}</span><Link to="/settings" onClick={()=>setAI(null)}>查看配置</Link></div>}
    {serverMode&&status?.teaching&&status.teaching.configured===false&&<div className="ai-task-note">教学服务尚未配置{status.teaching.reason?`（${status.teaching.reason}）`:''}；任务将由服务器模型网关直接执行。</div>}
    <div className="chat-scroll" ref={scroll}>{!messages.length&&<div className="ai-welcome"><div className="sparkle-orbit"><Sparkles size={30}/></div><h3>{context.hint?'先给一点启发':'从这里开始，一起理解'}</h3><p>我会结合当前{context.kind==='question'?'题目':context.kind==='essay'?'论文草稿':context.kind==='case'?'案例作答':'知识章节'}，解释概念、提供反馈，并给出参考资料。</p><div className="prompt-list">{prompts.map(prompt=><button key={prompt} disabled={busy||!loaded} onClick={()=>void send(prompt)}>{prompt}<ArrowUp size={15}/></button>)}</div><Link to="/settings" onClick={()=>setAI(null)} className="text-link"><Settings2 size={14}/> 首次使用？配置 AI 接口</Link></div>}
      {messages.map(message=><div className={`chat-message ${message.role}`} key={message.id}><div className="message-label">{message.role==='user'?'你':'学习搭子'}</div><Markdown>{message.content}</Markdown>{message.sources&&<div className="citations">{message.sources.filter(source=>source.materialId||message.content.includes(`[${source.id}]`)).map(source=>source.materialId?<Link key={source.id} to="/knowledge" search={{id:source.materialId,...(source.materialVersionId?{version:source.materialVersionId}:{}),...(source.sectionId?{section:source.sectionId}:{})}} onClick={()=>setAI(null)}><BookOpen size={12}/> [{source.id}] {source.title}</Link>:<span key={source.id}>[{source.id}] {source.title}</span>)}</div>}</div>)}
      {(busy||stream)&&<div className="chat-message assistant streaming"><div className="message-label">学习搭子 {busy&&<span className="typing-dots">•••</span>}</div><Markdown>{stream||'正在检查任务与资料…'}</Markdown>{waitingQuestion&&<p className="ai-waiting-question">{waitingQuestion}</p>}</div>}
      {error&&<div className="inline-error" role="alert">{error}<Link to="/settings" onClick={()=>setAI(null)}>前往配置</Link></div>}
    </div>
    <form className="chat-composer" onSubmit={event=>{event.preventDefault();void send(input);}}><textarea aria-label="向 AI 提问" value={input} onChange={event=>setInput(event.target.value)} maxLength={12000} placeholder={taskId?'继续追问，或补充你的思路…':'说说你的思路，或继续追问…'} rows={2} onKeyDown={event=>{if(event.key==='Enter'&&!event.shiftKey&&!event.nativeEvent.isComposing){event.preventDefault();void send(input);}}}/><div><span>Enter 发送 · Shift Enter 换行</span>{busy?<Button type="button" variant="secondary" onClick={()=>void cancel()} aria-label="停止生成"><Square size={15}/></Button>:<Button aria-label="发送" disabled={!input.trim()||!loaded} type="submit"><ArrowUp size={18}/></Button>}</div></form>
    <div className="chat-footer"><span>{serverMode?'任务在服务器执行；断线后用事件序号续接':'对话自动保存在本机'}</span><button disabled={busy||!loaded||!messages.length} onClick={async()=>{try{await db.chats.delete(context.id);setMessages([]);setStream('');setError('');setTaskId('');notify('当前对话已清空。');}catch(caught){setError(errorText(caught));}}}><RotateCcw size={12}/> 清空对话</button></div>
  </>;
}
