import {KnowledgeIndexes} from '../components/KnowledgeIndexes';
import {useEffect,useState,type FormEvent} from 'react';
import {Activity,AlertTriangle,Archive,ClipboardList,DatabaseBackup,Plus,RefreshCw,ShieldCheck,Trash2,UserPlus,Users} from 'lucide-react';
import {useAuth} from '../auth';
import {api,apiDelete,apiGet,apiPatch,apiPost,apiPut,errorText} from '../api';
import {Badge,Button,Empty,Loading,PageTitle} from '../ui';

type Tab='overview'|'users'|'grants'|'models'|'tasks'|'audit'|'backups';
const tabs:[Tab,string][]=[['overview','状态'],['users','用户与邀请'],['grants','资源授权'],['models','模型配置'],['tasks','任务'],['audit','审计'],['backups','备份与保留']];
const listOf=(raw:unknown,key:string):Record<string,unknown>[]=>{
  if(Array.isArray(raw)) return raw.filter((item):item is Record<string,unknown>=>!!item&&typeof item==='object');
  if(raw&&typeof raw==='object'){const value=(raw as Record<string,unknown>)[key];if(Array.isArray(value))return value.filter((item):item is Record<string,unknown>=>!!item&&typeof item==='object');}
  return [];
};
const statusTone=(value:unknown)=>value==='ok'||value==='ready'||value==='active'||value==='verified'||value==='completed'?'green':value==='failed'||value==='error'||value==='disabled'?'amber':'neutral';
const stamp=(value:unknown)=>{const text=String(value||'');return text?text.slice(0,19).replace('T',' '):'—';};

export function Admin(){
  const {capabilities}=useAuth();
  const [tab,setTab]=useState<Tab>('overview');
  const [data,setData]=useState<Record<string,unknown>>({});
  const [loading,setLoading]=useState(false);
  const [error,setError]=useState('');
  const [notice,setNotice]=useState('');
  const load=async(next:Tab=tab)=>{
    setLoading(true);setError('');
    const paths:Record<Tab,string>={overview:'/api/admin/status',users:'/api/admin/users',grants:'/api/admin/grants',models:'/api/admin/model-configs',tasks:'/api/admin/tasks?limit=100',audit:'/api/admin/audit?limit=100',backups:'/api/admin/backups'};
    try{
      const result=await apiGet<unknown>(paths[next]);
      const merged:Record<string,unknown>=result&&typeof result==='object'?result as Record<string,unknown>:{items:result};
      if(next==='backups'){const retention=await apiGet<unknown>('/api/admin/retention');merged.retention=retention;}
      setData(merged);
    }catch(caught){setError(errorText(caught));}
    finally{setLoading(false);}
  };
  useEffect(()=>{void load(tab);},[tab]);
  if(!capabilities.admin) return <div className="page-enter"><PageTitle eyebrow="ADMIN" title="管理后台" description="只有管理员可以访问此页面。"/><Empty title="没有访问权限">请联系管理员开通管理权限。</Empty></div>;
  return <div className="page-enter admin-page"><PageTitle eyebrow="ADMIN CONSOLE" title="平台管理" description="账号、授权、模型、任务、审计与备份集中管理；状态分开显示，失败原因保持可见。"/><div className="admin-tabs" role="tablist" aria-label="管理后台栏目">{tabs.map(([key,label])=><button role="tab" aria-selected={tab===key} key={key} className={tab===key?'active':''} onClick={()=>setTab(key)}>{label}</button>)}</div>{error&&<div className="inline-error" role="alert">{error}<button onClick={()=>void load()}>重新加载</button></div>}{notice&&<div className="inline-success" role="status">{notice}</div>}{loading?<Loading/>:<div className="admin-body">
    {tab==='overview'&&<><Overview data={data}/><KnowledgeIndexes admin/></>}
    {tab==='users'&&<UsersPanel data={data} reload={()=>void load('users')} notify={setNotice} error={setError}/>}
    {tab==='grants'&&<GrantsPanel data={data} reload={()=>void load('grants')} notify={setNotice} error={setError}/>}
    {tab==='models'&&<ModelsPanel data={data} reload={()=>void load('models')} notify={setNotice} error={setError}/>}
    {tab==='tasks'&&<TasksPanel data={data}/>}
    {tab==='audit'&&<AuditPanel data={data}/>}
    {tab==='backups'&&<BackupsPanel data={data} reload={()=>void load('backups')} notify={setNotice} error={setError}/>}
  </div>}</div>;
}

function Overview({data}:{data:Record<string,unknown>}){
  const checks=data.checks&&typeof data.checks==='object'?data.checks as Record<string,unknown>:{};
  const keys=['process','database','teaching','model','index'];
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><Activity size={17}/> 服务健康</h3><Badge tone={statusTone(data.status)}>{String(data.status||'未知')}</Badge></div><div className="health-grid">{keys.map(key=><div className="health-card" key={key}><span>{key}</span><strong>{String(checks[key]??'—')}</strong></div>)}</div>{typeof data.version==='string'&&<p className="muted-text">版本 {data.version}</p>}</section></div>;
}

function UsersPanel({data,reload,notify,error}:{data:Record<string,unknown>;reload:()=>void;notify:(text:string)=>void;error:(text:string)=>void}){
  const users=listOf(data.users??data,'users');
  const [email,setEmail]=useState(''),[displayName,setDisplayName]=useState(''),[role,setRole]=useState('learner'),[password,setPassword]=useState('');
  const [inviteEmail,setInviteEmail]=useState(''),[inviteRole,setInviteRole]=useState('learner'),[inviteToken,setInviteToken]=useState('');
  async function createUser(event:FormEvent){event.preventDefault();try{await apiPost('/api/admin/users',{email:email.trim(),displayName:displayName.trim(),role,password});setEmail('');setDisplayName('');setPassword('');notify('账号已创建，首次登录需要修改密码。');reload();}catch(caught){error(errorText(caught));}}
  async function createInvite(event:FormEvent){event.preventDefault();try{const result=await apiPost<{token?:string}>('/api/auth/invites',{email:inviteEmail.trim(),role:inviteRole});setInviteToken(result.token||'');setInviteEmail('');notify('邀请已创建，一次性令牌只显示一次。');}catch(caught){error(errorText(caught));}}
  async function patchUser(id:string,body:Record<string,unknown>){try{await apiPatch(`/api/admin/users/${encodeURIComponent(id)}`,body);notify('账号已更新，权限在下一个请求生效。');reload();}catch(caught){error(errorText(caught));}}
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><Users size={17}/> 账号列表</h3><small>{users.length} 个账号</small></div><div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>邮箱</th><th>名称</th><th>角色</th><th>状态</th><th>最近登录</th><th>操作</th></tr></thead><tbody>{users.map(user=>{const id=String(user.id||'');return <tr key={id}><td>{String(user.email||'')}</td><td>{String(user.displayName||'')}</td><td><select aria-label={`角色 ${String(user.email||id)}`} value={String(user.role||'learner')} onChange={event=>void patchUser(id,{role:event.target.value})}><option value="learner">learner</option><option value="admin">admin</option></select></td><td><Badge tone={statusTone(user.status)}>{String(user.status||'')}</Badge></td><td>{stamp(user.lastLoginAt)}</td><td><Button variant="secondary" onClick={()=>void patchUser(id,{status:user.status==='active'?'disabled':'active'})}>{user.status==='active'?'停用':'启用'}</Button><Button variant="ghost" onClick={()=>{const next=window.prompt('输入新密码（至少 10 位，包含字母与数字）');if(next)void patchUser(id,{newPassword:next});}}>重置密码</Button></td></tr>;})}</tbody></table>{!users.length&&<Empty title="还没有账号">创建第一个学习者账号或发送邀请。</Empty>}</div></section>
    <div className="admin-two-col"><section className="panel"><div className="section-heading"><h3><UserPlus size={17}/> 直接创建账号</h3></div><form className="settings-form admin-form" onSubmit={createUser}><label>邮箱<input aria-label="新账号邮箱" type="email" required value={email} onChange={event=>setEmail(event.target.value)}/></label><label>显示名称<input aria-label="新账号名称" value={displayName} onChange={event=>setDisplayName(event.target.value)}/></label><label>角色<select aria-label="新账号角色" value={role} onChange={event=>setRole(event.target.value)}><option value="learner">learner</option><option value="admin">admin</option></select></label><label>初始密码<input aria-label="新账号密码" type="password" required value={password} onChange={event=>setPassword(event.target.value)}/></label><Button type="submit">创建账号</Button></form></section>
    <section className="panel"><div className="section-heading"><h3><UserPlus size={17}/> 创建邀请</h3></div><form className="settings-form admin-form" onSubmit={createInvite}><label>邀请邮箱<input aria-label="邀请邮箱" type="email" required value={inviteEmail} onChange={event=>setInviteEmail(event.target.value)}/></label><label>角色<select aria-label="邀请角色" value={inviteRole} onChange={event=>setInviteRole(event.target.value)}><option value="learner">learner</option><option value="admin">admin</option></select></label><Button type="submit">生成邀请令牌</Button></form>{inviteToken&&<div className="token-box" role="status"><span>邀请令牌（只显示一次）</span><code>{inviteToken}</code><Button variant="secondary" onClick={()=>{void navigator.clipboard?.writeText(inviteToken);notify('邀请令牌已复制。');}}>复制</Button></div>}</section></div>
  </div>;
}

function GrantsPanel({data,reload,notify,error}:{data:Record<string,unknown>;reload:()=>void;notify:(text:string)=>void;error:(text:string)=>void}){
  const grants=listOf(data.grants??data,'grants');
  const [userId,setUserId]=useState(''),[resourceType,setResourceType]=useState('material'),[resourceId,setResourceId]=useState('');
  async function create(event:FormEvent){event.preventDefault();try{await apiPost('/api/admin/grants',{userId:userId.trim(),resourceType,resourceId:resourceId.trim()});setUserId('');setResourceId('');notify('授权已创建，下一个请求生效。');reload();}catch(caught){error(errorText(caught));}}
  async function revoke(id:string){try{await apiDelete(`/api/admin/grants/${encodeURIComponent(id)}`);notify('授权已撤销。');reload();}catch(caught){error(errorText(caught));}}
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><ShieldCheck size={17}/> 共享资源授权</h3><small>{grants.length} 条</small></div><div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>用户</th><th>资源类型</th><th>资源 ID</th><th>创建时间</th><th>操作</th></tr></thead><tbody>{grants.map(grant=><tr key={String(grant.id)}><td>{String(grant.email||grant.userEmail||grant.userId||'')}</td><td>{String(grant.resourceType||'')}</td><td>{String(grant.resourceId||'')}</td><td>{stamp(grant.createdAt)}</td><td><Button variant="ghost" onClick={()=>void revoke(String(grant.id))}><Trash2 size={14}/>撤销</Button></td></tr>)}</tbody></table>{!grants.length&&<Empty title="还没有授权">共享知识库通常对所有学习者开放；此处用于额外授权。</Empty>}</div></section><section className="panel"><div className="section-heading"><h3><Plus size={17}/> 新增授权</h3></div><form className="settings-form admin-form" onSubmit={create}><label>用户 ID<input aria-label="授权用户 ID" required value={userId} onChange={event=>setUserId(event.target.value)}/></label><label>资源类型<select aria-label="授权资源类型" value={resourceType} onChange={event=>setResourceType(event.target.value)}><option value="material">material</option><option value="collection">collection</option><option value="capability">capability</option></select></label><label>资源 ID<input aria-label="授权资源 ID" required value={resourceId} onChange={event=>setResourceId(event.target.value)}/></label><Button type="submit">创建授权</Button></form></section></div>;
}

function ModelsPanel({data,reload,notify,error}:{data:Record<string,unknown>;reload:()=>void;notify:(text:string)=>void;error:(text:string)=>void}){
  const configs=listOf(data.items??data,'items');
  const environment=(data.environment&&typeof data.environment==='object'?data.environment:{}) as Record<string,unknown>;
  const [selected,setSelected]=useState(''),[endpoint,setEndpoint]=useState(''),[model,setModel]=useState(''),[apiKey,setApiKey]=useState(''),[gatewayName,setGatewayName]=useState(''),[provider,setProvider]=useState('');
  const [editingNew,setEditingNew]=useState(false),[newName,setNewName]=useState(''),[newKind,setNewKind]=useState('llm'),[newEndpoint,setNewEndpoint]=useState(''),[newModel,setNewModel]=useState(''),[newApiKey,setNewApiKey]=useState('');
  async function create(event:FormEvent){event.preventDefault();try{await apiPost('/api/admin/model-configs',{name:newName.trim(),kind:newKind,provider:'openai-compatible',endpoint:newEndpoint.trim(),model:newModel.trim(),...(newApiKey.trim()?{apiKey:newApiKey.trim()}:{})});setNewName('');setNewEndpoint('');setNewModel('');setNewApiKey('');setEditingNew(false);notify('模型配置已创建；凭据只写入，不读取。');reload();}catch(caught){error(errorText(caught));}}
  async function save(event:FormEvent){event.preventDefault();if(!selected)return;const body:Record<string,unknown>={endpoint,model};if(gatewayName.trim())body.name=gatewayName.trim();if(provider.trim())body.provider=provider.trim();if(apiKey.trim())body.apiKey=apiKey.trim();try{await apiPut(`/api/admin/model-configs/${encodeURIComponent(selected)}`,body);setApiKey('');notify('模型配置已更新；凭据只写入，不读取。');reload();}catch(caught){error(errorText(caught));}}
  async function check(id:string){try{await apiPost(`/api/admin/model-configs/${encodeURIComponent(id)}/check`,{});notify('连接检查已执行，结果以服务器状态为准。');reload();}catch(caught){error(errorText(caught));}}
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><Activity size={17}/> 模型配置</h3><div className="button-row"><Button variant="secondary" onClick={()=>setEditingNew(current=>!current)}><Plus size={14}/>新增配置</Button><Button variant="ghost" onClick={reload}><RefreshCw size={14}/>刷新</Button></div></div><div className="environment-strip"><span>服务器默认环境</span><Badge tone={environment.configured===true?'green':'neutral'}>{environment.configured===true?'已配置':'未配置'}</Badge><code>{String(environment.gatewayName||'服务器 AI 网关')}</code><span>{String(environment.model||'未设置模型')} · 鉴权 {environment.authConfigured===true?'已配置':'未配置'}</span></div><div className="model-grid">{configs.map(config=>{const id=String(config.id||'');return <article className="model-card" key={id}><div className="model-card-head"><strong>{String(config.name||config.gatewayName||config.provider||id)}</strong><Badge tone={config.configured===true?'green':'neutral'}>{config.configured===true?'已配置':'未配置'}</Badge></div><dl><div><dt>网关</dt><dd>{String(config.gatewayName||'—')}</dd></div><div><dt>接口</dt><dd>{String(config.endpoint||'—')}</dd></div><div><dt>模型</dt><dd>{String(config.model||'—')}</dd></div></dl><div className="status-triplet"><div className={`status-tile ${config.configured===true?'ok':'warn'}`}><span>配置状态</span><strong>{config.configured===true?'已配置':'未配置'}</strong></div><div className={`status-tile ${config.connectionStatus==='ok'?'ok':config.connectionStatus==='failed'?'bad':'warn'}`}><span>连接状态</span><strong>{String(config.connectionStatus||'未知')}</strong></div><div className={`status-tile ${config.capabilityStatus==='verified'?'ok':config.capabilityStatus==='failed'?'bad':'warn'}`}><span>能力验证</span><strong>{String(config.capabilityStatus||'未验证')}</strong></div></div>{config.unavailableReason?<p className="inline-warning"><AlertTriangle size={14}/>{String(config.unavailableReason)}</p>:null}<div className="button-row"><Button variant="secondary" onClick={()=>void check(id)}>连接检查</Button><Button variant="ghost" onClick={()=>{setSelected(id);setEndpoint(String(config.endpoint||''));setModel(String(config.model||''));setGatewayName(String(config.name||config.gatewayName||''));setProvider(String(config.provider||''));}}>编辑配置</Button></div></article>;})}{!configs.length&&<Empty title="没有模型配置">模型配置由服务器管理；配置后这里会显示真实状态。</Empty>}</div></section>{editingNew&&<section className="panel"><div className="section-heading"><h3>新增模型配置</h3></div><form className="settings-form admin-form" onSubmit={create}><label>配置名称<input aria-label="新增配置名称" required value={newName} onChange={event=>setNewName(event.target.value)} placeholder="例如：服务器默认网关"/></label><label>模型类型<select aria-label="新增配置类型" value={newKind} onChange={event=>setNewKind(event.target.value)}><option value="llm">llm</option><option value="embedding">embedding</option><option value="rerank">rerank</option></select></label><label>接口地址<input aria-label="新增配置接口地址" value={newEndpoint} onChange={event=>setNewEndpoint(event.target.value)} placeholder="https://…/v1/chat/completions"/></label><label>模型 / 路由组<input aria-label="新增配置模型" value={newModel} onChange={event=>setNewModel(event.target.value)}/></label><label>凭据（只写不读）<input aria-label="新增配置凭据" type="password" autoComplete="off" value={newApiKey} onChange={event=>setNewApiKey(event.target.value)}/></label><Button type="submit">创建配置</Button></form></section>}
    {selected&&<section className="panel"><div className="section-heading"><h3>编辑配置</h3><small>{selected}</small></div><form className="settings-form admin-form" onSubmit={save}><label>配置名称<input aria-label="模型配置名称" value={gatewayName} onChange={event=>setGatewayName(event.target.value)}/></label><label>供应商标识<input aria-label="模型供应商标识" value={provider} onChange={event=>setProvider(event.target.value)}/></label><label>接口地址<input aria-label="模型接口地址" value={endpoint} onChange={event=>setEndpoint(event.target.value)}/></label><label>模型 / 路由组<input aria-label="模型路由组" value={model} onChange={event=>setModel(event.target.value)}/></label><label>凭据（只写不读）<input aria-label="模型凭据" type="password" autoComplete="off" value={apiKey} onChange={event=>setApiKey(event.target.value)} placeholder="留空表示不修改"/></label><Button type="submit">保存配置</Button></form></section>}</div>;
}

function TasksPanel({data}:{data:Record<string,unknown>}){
  const tasks=listOf(data.items??data,'items');
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><ClipboardList size={17}/> 后台任务</h3><small>{tasks.length} 条</small></div><div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>任务</th><th>来源</th><th>类型</th><th>状态</th><th>用户</th><th>尝试</th><th>耗时</th><th>失败原因</th><th>创建时间</th></tr></thead><tbody>{tasks.map(task=><tr key={String(task.id)}><td className="mono">{String(task.id||'').slice(0,8)}</td><td>{String(task.source||'')}</td><td>{String(task.kind||'')}</td><td><Badge tone={statusTone(task.status)}>{String(task.status||'')}</Badge></td><td>{String(task.userEmail||'')}</td><td>{task.attempts!=null?String(task.attempts):'—'}{task.retryCount?`（重试 ${String(task.retryCount)}）`:''}</td><td>{task.durationMs!=null?`${Math.round(Number(task.durationMs))} ms`:'—'}</td><td className="error-cell">{String(task.errorMessage||task.errorText||'')}</td><td>{stamp(task.createdAt)}</td></tr>)}</tbody></table>{!tasks.length&&<Empty title="暂无后台任务">AI 任务与内容导入任务会显示在这里。</Empty>}</div></section></div>;
}

function AuditPanel({data}:{data:Record<string,unknown>}){
  const entries=listOf(data.items??data,'items');
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><ClipboardList size={17}/> 审计日志</h3><small>{entries.length} 条</small></div><div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>时间</th><th>操作者</th><th>动作</th><th>对象</th><th>详情</th></tr></thead><tbody>{entries.map(entry=><tr key={String(entry.id)}><td>{stamp(entry.createdAt||entry.time)}</td><td>{String(entry.actorUserId||entry.actorEmail||entry.actorId||'—')}</td><td>{String(entry.action||'')}</td><td>{String(entry.targetType||'')} {String(entry.targetId||'')}</td><td className="error-cell">{entry.detail?JSON.stringify(entry.detail).slice(0,140):''}</td></tr>)}</tbody></table>{!entries.length&&<Empty title="暂无审计记录">登录、授权、任务与管理操作会记录在这里。</Empty>}</div></section></div>;
}

function BackupsPanel({data,reload,notify,error}:{data:Record<string,unknown>;reload:()=>void;notify:(text:string)=>void;error:(text:string)=>void}){
  const backups=listOf(data.items??data,'items');
  const retentionWrapper=data.retention&&typeof data.retention==='object'?data.retention as Record<string,unknown>:{};
  const retention=(retentionWrapper.policy&&typeof retentionWrapper.policy==='object'?retentionWrapper.policy:{}) as Record<string,unknown>;
  const [retentionForm,setRetentionForm]=useState<Record<string,string>>({});
  useEffect(()=>{setRetentionForm({tasks:String(retention.tasks??90),events:String(retention.events??30),audit:String(retention.audit??365),backups:String(retention.backups??30),logs:String(retention.logs??30)});},[retention.tasks,retention.events,retention.audit,retention.backups,retention.logs]);
  async function create(){try{await apiPost('/api/admin/backups',{});notify('备份任务已创建，完成后可校验。');reload();}catch(caught){error(errorText(caught));}}
  async function action(id:string,kind:'verify'|'restore'){
    // Restoring needs a brand-new target directory: the server refuses an empty
    // target and refuses to overwrite the live data root, so ask for it instead
    // of sending an empty body that can only fail. The suggested path makes the
    // usual drill (./data/restore-<timestamp>) one keystroke away.
    let body:Record<string,unknown>={};
    if(kind==='restore'){
      const suggested=`./data/restore-${new Date().toISOString().replace(/[-:]/g,'').slice(0,15)}`;
      const target=window.prompt(`恢复到哪个新目录？（必须是空目录或不存在；不会覆盖现有数据）\n提示：不能是当前数据根或备份目录`,suggested);
      if(!target||!target.trim()) return;
      body={targetDir:target.trim()};
    }
    try{const result=await apiPost<Record<string,unknown>>(`/api/admin/backups/${encodeURIComponent(id)}/${kind}`,body);notify(kind==='verify'?`备份校验完成：${(result.ok===true?'通过':String(result.status||result.result||'完成'))}`:`已恢复到新目录：${String(result.targetDir||result.targetPath||result.path||'请查看服务器日志')}（现有数据未被覆盖）`);reload();}catch(caught){error(errorText(caught));}
  }
  async function saveRetention(event:FormEvent){event.preventDefault();try{const body={tasks:Number(retentionForm.tasks||0),events:Number(retentionForm.events||0),audit:Number(retentionForm.audit||0),backups:Number(retentionForm.backups||0),logs:Number(retentionForm.logs||0)};await apiPut('/api/admin/retention',body);notify('保留策略已更新。');reload();}catch(caught){error(errorText(caught));}}
  return <div className="admin-section"><section className="panel"><div className="section-heading"><h3><DatabaseBackup size={17}/> 备份</h3><Button onClick={()=>void create()}><Archive size={15}/>创建一致性备份</Button></div><div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>备份</th><th>创建时间</th><th>状态</th><th>大小</th><th>操作</th></tr></thead><tbody>{backups.map(backup=>{const id=String(backup.id||'');return <tr key={id}><td className="mono">{id}</td><td>{stamp(backup.createdAt)}</td><td><Badge tone={statusTone(backup.status)}>{String(backup.status||'')}</Badge></td><td>{backup.sizeBytes!=null?`${Math.round(Number(backup.sizeBytes)/1024/1024*10)/10} MB`:'—'}</td><td><Button variant="secondary" onClick={()=>void action(id,'verify')}>校验</Button><Button variant="ghost" onClick={()=>void action(id,'restore')}>恢复到新目录</Button></td></tr>;})}</tbody></table>{!backups.length&&<Empty title="还没有备份">创建备份后会在项目 data/backups 目录生成批次。</Empty>}</div></section><section className="panel"><div className="section-heading"><h3>保留策略</h3></div><form className="settings-form admin-form" onSubmit={saveRetention}>{[['tasks','任务保留天数'],['events','AI 事件保留天数'],['audit','审计保留天数'],['backups','备份保留天数'],['logs','日志保留天数']].map(([key,label])=><label key={key}>{label}<input aria-label={label} type="number" min="0" value={retentionForm[key]??''} onChange={event=>setRetentionForm(current=>({...current,[key]:event.target.value}))}/></label>)}<Button type="submit">保存保留策略</Button></form></section></div>;
}
