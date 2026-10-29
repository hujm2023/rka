import {useEffect,useRef,useState} from 'react';
import {Download,Upload,Database,ShieldCheck,Sparkles,ExternalLink,CheckCircle2,Settings2,MonitorSmartphone,KeyRound,Trash2,Moon,Sun} from 'lucide-react';
import {useApp} from '../context';
import {useAuth} from '../auth';
import {AIConnectionSettings} from '../components/AIConnectionSettings';
import {api,apiDelete,apiGet,apiPost,errorText} from '../api';
import {convertLegacy,validateBackup} from '../db';
import {syncDb} from '../sync';
import {Badge,Button,Modal,PageTitle,SourceLink} from '../ui';

type PendingImport={payload:unknown;legacy:boolean;summary:string};

export function Settings(){
  const {notify,catalog,attempts,marks,reading,theme,toggleTheme}=useApp();
  const {user,changePassword,logout}=useAuth();
  const [pending,setPending]=useState<PendingImport|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false),[persistent,setPersistent]=useState(false);
  const [sessions,setSessions]=useState<Record<string,unknown>[]>([]),[sessionError,setSessionError]=useState('');
  const [currentPassword,setCurrentPassword]=useState(''),[newPassword,setNewPassword]=useState(''),[confirmPassword,setConfirmPassword]=useState(''),[passwordStatus,setPasswordStatus]=useState('');
  const [deleteOpen,setDeleteOpen]=useState(false),[deleteConfirm,setDeleteConfirm]=useState('');
  const fileRef=useRef<HTMLInputElement>(null);
  useEffect(()=>{navigator.storage?.persisted?.().then(setPersistent).catch(()=>{});},[]);
  const loadSessions=async()=>{try{const raw=await apiGet<{sessions?:Record<string,unknown>[]}>('/api/auth/sessions');setSessions(raw.sessions||[]);setSessionError('');}catch(caught){setSessionError(errorText(caught));}};
  useEffect(()=>{void loadSessions();},[]);

  async function exportData(){
    setError('');
    try{
      const payload=await apiGet<unknown>('/api/export/me');
      const blob=new Blob([JSON.stringify(payload,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),anchor=document.createElement('a');
      anchor.href=url;anchor.download=`rka-export-${new Date().toISOString().slice(0,10)}.json`;anchor.click();
      setTimeout(()=>URL.revokeObjectURL(url),1000);
      notify('学习数据已从服务器导出。');
    }catch(caught){setError(`导出失败：${errorText(caught)}`);}
  }
  async function readFile(file?:File){
    if(!file) return;setError('');
    try{
      if(file.size>50*1024*1024) throw new Error('备份文件超过 50 MB，未导入。');
      const payload=JSON.parse(await file.text()) as Record<string,unknown>;
      if(payload.app==='rka'&&payload.schemaVersion===1){
        const data=validateBackup(payload);
        setPending({payload,legacy:false,summary:`${data.attempts.length} 条作答、${data.marks.length} 条标记、${data.drafts.length} 份草稿、${data.reading.length} 条阅读进度、${data.chats.length} 组 AI 对话。`});
      }else if(payload.app==='rka'){
        const counts=payload.counts&&typeof payload.counts==='object'?payload.counts as Record<string,number>:{};
        setPending({payload,legacy:false,summary:`服务器导出数据（schema v${String(payload.schemaVersion)}）：${Object.entries(counts).slice(0,6).map(([key,value])=>`${key} ${value}`).join('、')}。导入由服务器校验并幂等处理。`});
      }else{
        // Legacy practice-platform files carry only attempts and bookmarks. Wrap
        // them in the v1 backup envelope the server understands; unmatched
        // question ids are dropped here and reported in the summary.
        const converted=convertLegacy(payload,new Set(catalog.choices.map(question=>question.id)));
        const wrapped={app:'rka',schemaVersion:1,exportedAt:new Date().toISOString(),data:{attempts:converted.attempts,marks:converted.marks,drafts:[],reading:[],sessions:[],chats:[]}};
        setPending({payload:wrapped,legacy:true,summary:`旧版进度文件：匹配到 ${converted.attempts.length} 条作答与 ${converted.marks.length} 条收藏，将由服务器校验后导入。`});
      }
    }catch(caught){setError(caught instanceof Error?caught.message:'文件解析失败');}
    finally{if(fileRef.current)fileRef.current.value='';}
  }
  async function restore(){
    if(!pending) return;setBusy(true);setError('');
    try{
      const result=await apiPost<{imported?:Record<string,number>;skipped?:unknown[];conflicts?:unknown[]}>('/api/sync/import',pending.payload);
      const counts=result.imported?Object.entries(result.imported).map(([key,value])=>`${key} ${value}`).join('、'):'';
      setPending(null);
      notify(`备份已导入${counts?`：${counts}`:''}${result.skipped?.length?`；${result.skipped.length} 条无法匹配已跳过`:''}。`);
    }catch(caught){setError(`导入失败：${errorText(caught)}`);}
    finally{setBusy(false);}
  }
  async function changePasswordNow(){
    setPasswordStatus('');
    if(newPassword!==confirmPassword){setPasswordStatus('两次输入的新密码不一致。');return;}
    try{await changePassword(currentPassword,newPassword);setCurrentPassword('');setNewPassword('');setConfirmPassword('');setPasswordStatus('密码已修改，其他设备会话已退出。');}
    catch(caught){setPasswordStatus(errorText(caught));}
  }
  async function revokeSession(id:string){
    try{await apiDelete(`/api/auth/sessions/${encodeURIComponent(id)}`);await loadSessions();notify(id&&sessions.find(session=>String(session.id)===id)?.current?'已退出当前设备。':'已退出该设备。');if(sessions.find(session=>String(session.id)===id)?.current)await logout();}
    catch(caught){setSessionError(errorText(caught));}
  }
  async function deleteMyData(){
    if(deleteConfirm!=='DELETE'){setError('请输入 DELETE 确认删除。');return;}
    setBusy(true);setError('');
    try{await api('/api/me/data',{method:'DELETE',json:{confirm:'DELETE'}});setDeleteOpen(false);notify('学习数据已删除，账号保留。');}
    catch(caught){setError(`删除失败：${errorText(caught)}`);}
    finally{setBusy(false);}
  }
  return <div className="page-enter settings-page"><PageTitle eyebrow="YOUR STUDY SPACE" title="你的学习，由你掌握。" description="管理账号、设备、学习数据与 AI 辅导配置。"/>
    <section className="panel settings-section"><div className="settings-heading"><span className="settings-icon"><ShieldCheck size={23}/></span><div><h2>账号与安全</h2><p>账号由服务器统一管理；密码与设备会话不会保存在浏览器。</p></div><Badge tone="green">{user?.role==='admin'?'管理员':'学习者'}</Badge></div>
      <div className="account-summary"><div><span>邮箱</span><strong>{user?.email}</strong></div><div><span>显示名称</span><strong>{user?.displayName}</strong></div><div><span>状态</span><strong>{user?.status==='active'?'正常':'已停用'}</strong></div></div>
      <div className="settings-form password-form"><label>当前密码<input aria-label="当前密码" type="password" autoComplete="current-password" value={currentPassword} onChange={event=>setCurrentPassword(event.target.value)}/></label><label>新密码<input aria-label="新密码" type="password" autoComplete="new-password" value={newPassword} onChange={event=>setNewPassword(event.target.value)}/><small>至少 10 位，包含字母与数字。</small></label><label>确认新密码<input aria-label="确认新密码" type="password" autoComplete="new-password" value={confirmPassword} onChange={event=>setConfirmPassword(event.target.value)}/></label></div>
      {passwordStatus&&<div className={`inline-${passwordStatus.startsWith('密码已修改')?'success':'error'}`} role="status">{passwordStatus}</div>}
      <div className="settings-action"><p><KeyRound size={15}/>修改密码后，其他设备需要重新登录。</p><Button variant="secondary" onClick={()=>void changePasswordNow()} disabled={!currentPassword||!newPassword}>修改密码</Button></div>
    </section>
    <section className="panel settings-section"><div className="settings-heading"><span className="settings-icon"><MonitorSmartphone size={23}/></span><div><h2>设备会话</h2><p>查看并退出已登录的设备；撤销在下一次请求立即生效。</p></div><Button variant="ghost" onClick={()=>void loadSessions()}>刷新</Button></div>
      {sessionError&&<div className="inline-error" role="alert">{sessionError}</div>}
      <div className="device-list">{sessions.map(session=>{const id=String(session.id||'');return <div className="device-row" key={id}><div><strong>{String(session.userAgent||'未知设备')}{session.current===true&&<Badge tone="green">当前设备</Badge>}</strong><span>{String(session.ip||'')} · 最近活动 {String(session.lastSeenAt||'').slice(0,19).replace('T',' ')} · 到期 {String(session.expiresAt||'').slice(0,10)}</span></div><Button variant="secondary" onClick={()=>void revokeSession(id)}>{session.current===true?'退出登录':'退出该设备'}</Button></div>;})}{!sessions.length&&!sessionError&&<p className="muted-text">正在读取设备会话…</p>}</div>
    </section>
    <AIConnectionSettings/>
    <section className="panel settings-section"><div className="settings-heading"><span className="settings-icon"><Database size={23}/></span><div><h2>学习进度与备份</h2><p>服务器保存学习数据；导出用于离线留存，导入与删除都由服务器执行。</p></div><Badge tone="green">服务器数据</Badge></div>
      <div className="data-summary"><span><strong>{attempts.length}</strong> 次本地缓存作答</span><span><strong>{marks.filter(mark=>mark.favorite).length}</strong> 道收藏</span><span><strong>{reading.filter(entry=>entry.done&&(catalog.materials||[]).some(material=>material.id===entry.id)).length}</strong> 份资料已学</span></div>
      <div className="backup-actions"><button className="backup-card" onClick={()=>void exportData()}><Download size={24}/><div><h3>导出完整备份</h3><p>从服务器导出全部学习数据（JSON）</p></div><ExternalLink size={16}/></button><button className="backup-card" onClick={()=>fileRef.current?.click()}><Upload size={24}/><div><h3>导入学习进度</h3><p>支持 RKA 备份与旧练题台进度，导入前会校验</p></div><ExternalLink size={16}/></button><input ref={fileRef} type="file" accept=".json,application/json" hidden onChange={event=>void readFile(event.target.files?.[0])}/></div>
      <div className="storage-row"><span>{persistent?'浏览器已授予持久存储（本地缓存更不易被清理）。':'本地缓存可能被浏览器回收；服务器上的学习数据不受影响，但仍建议定期导出。'}</span><Button variant="secondary" disabled={persistent||!navigator.storage?.persist} onClick={async()=>{try{const granted=await navigator.storage.persist();setPersistent(granted);notify(granted?'已启用持久存储。':'浏览器暂未授予持久存储，请继续使用导出备份。');}catch{setError('无法申请持久存储。');}}}>{persistent?'已启用':'申请持久存储'}</Button></div>
      <div className="danger-zone"><div><h3>删除我的学习数据</h3><p>删除作答、草稿、笔记、阅读记录与 AI 任务，保留账号。此操作不可撤销。</p></div><Button variant="danger" onClick={()=>setDeleteOpen(true)}><Trash2 size={15}/>删除我的数据</Button></div>
    </section>
    <section className="panel settings-section"><div className="settings-heading"><span className="settings-icon">{theme==='light'?<Sun size={23}/>:<Moon size={23}/>}</span><div><h2>外观</h2><p>主题保存在本机，阅读与图示会随主题切换。</p></div><Badge>{theme==='light'?'浅色':'深色'}</Badge></div><div className="theme-row"><Button variant={theme==='light'?'primary':'secondary'} onClick={()=>{if(theme!=='light')toggleTheme();}}><Sun size={15}/>浅色</Button><Button variant={theme==='dark'?'primary':'secondary'} onClick={()=>{if(theme!=='dark')toggleTheme();}}><Moon size={15}/>深色</Button></div></section>
    <section className="panel settings-section"><div className="settings-heading"><span className="settings-icon"><Settings2 size={23}/></span><div><h2>内容来源与版本</h2><p>应用独立实现；题库与学习资料保留原始来源和许可。</p></div></div><div className="source-list"><div><h3>ruankao-architect-practice</h3><p>基础题库、案例与论文材料。第三方题目不属于应用代码的 MIT 许可。</p><SourceLink href="/content/licenses/practice-NOTICE.md">来源声明</SourceLink></div><div><h3>ruankao-architect-notes</h3><p>主要知识手册与专题资料 · CC BY-NC-SA 4.0。</p><SourceLink href="/content/licenses/ruankao-architect-notes.txt">许可与署名</SourceLink></div><div><h3>awesome-ruankao</h3><p>2026 年回忆题、模拟卷和备考方法 · 仓库声明 CC BY-SA 4.0；真题保留原权利方声明。</p><SourceLink href="/content/licenses/awesome-ruankao.txt">许可与署名</SourceLink></div></div><div className="content-version">服务器内容版本 {catalog.version||'尚未加载'}</div></section>
    {error&&<div className="inline-error" role="alert">{error}</div>}
    <Modal open={!!pending} onOpenChange={value=>{if(!value&&!busy)setPending(null);}} title={pending?.legacy?'导入旧版学习记录？':'导入这份备份？'} description="文件先在本机校验，再由服务器幂等导入；无法匹配的题目会列出原因，不会静默丢弃。"><p>{pending?.summary}</p><div className="modal-actions"><Button variant="secondary" onClick={()=>setPending(null)}>取消</Button><Button disabled={busy} onClick={()=>void restore()}>{busy?'正在导入…':'确认恢复'}</Button></div></Modal>
    <Modal open={deleteOpen} onOpenChange={setDeleteOpen} title="删除我的学习数据？" description="账号会保留，但作答、草稿、笔记、阅读记录与 AI 任务都会从服务器删除。请输入 DELETE 确认。"><label>确认输入<input aria-label="删除确认" value={deleteConfirm} onChange={event=>setDeleteConfirm(event.target.value)} placeholder="DELETE"/></label><div className="modal-actions"><Button variant="secondary" onClick={()=>setDeleteOpen(false)}>取消</Button><Button variant="danger" disabled={busy||deleteConfirm!=='DELETE'} onClick={()=>void deleteMyData()}>确认删除</Button></div></Modal>
  </div>;
}
