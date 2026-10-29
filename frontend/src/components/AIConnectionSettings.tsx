import {useEffect,useState} from 'react';
import {AlertTriangle,CheckCircle2,RefreshCw,Server,ShieldCheck,Sparkles} from 'lucide-react';
import {useApp} from '../context';
import {errorText} from '../api';
import {Badge,Button} from '../ui';

type AIStatus={
  configured?:boolean;gatewayName?:string;endpoint?:string;model?:string;authConfigured?:boolean;
  connectionStatus?:string;capabilityStatus?:string;unavailableReason?:string;
  teaching?:{configured?:boolean;reachable?:boolean;reason?:string};
  budget?:{dailyLimit?:number;usedToday?:number};concurrency?:{limit?:number;active?:number};
};
const isServer=(endpoint:string)=>!endpoint.trim()||endpoint.trim()==='/api/ai';
const connectionLabel=(value?:string)=>value==='ok'?'连接正常':value==='failed'?'连接失败':value==='unknown'?'尚未检查':'未检查';
const capabilityLabel=(value?:string)=>value==='verified'?'能力已验证':value==='failed'?'能力验证失败':value==='unverified'?'能力未验证':'未验证';

export function AIConnectionSettings(){
  const {settings,setSettings,apiKey,setApiKey}=useApp();
  const activeMode=isServer(settings.endpoint)?'server':'custom';
  const [mode,setMode]=useState<'server'|'custom'>(activeMode);
  const [endpoint,setEndpoint]=useState(activeMode==='custom'?settings.endpoint:'');
  const [model,setModel]=useState(activeMode==='custom'?settings.model:'');
  const [gateway,setGateway]=useState<AIStatus|null>(null);
  const [loading,setLoading]=useState(true),[loadError,setLoadError]=useState(''),[formError,setFormError]=useState(''),[refresh,setRefresh]=useState(0);
  useEffect(()=>{
    const controller=new AbortController();setLoading(true);setLoadError('');
    fetch('/api/ai/status',{signal:controller.signal,cache:'no-store',credentials:'same-origin'}).then(async response=>{
      if(!response.ok)throw new Error(`读取服务器配置失败（${response.status}）`);
      return response.json() as Promise<AIStatus>;
    }).then(setGateway).catch(caught=>{if(!controller.signal.aborted){setGateway(null);setLoadError(errorText(caught));}}).finally(()=>{if(!controller.signal.aborted)setLoading(false);});
    return()=>controller.abort();
  },[refresh]);
  const unsaved=mode!==activeMode||(mode==='custom'&&(endpoint.trim()!==settings.endpoint||model.trim()!==settings.model));
  const configured=gateway?.configured===true;
  const status=unsaved?'切换尚未保存':activeMode==='custom'?'当前使用自定义接口':loading?'正在读取网关配置':loadError?'网关配置读取失败':configured?`服务器网关已配置`:`服务器尚未配置网关`;
  const websiteEndpoint=new URL('/api/ai',window.location.origin).href;
  function save(){
    setFormError('');
    if(mode==='server'){setSettings({endpoint:'/api/ai',model:''});setApiKey('');return;}
    try{
      const url=new URL(endpoint.trim());
      if(!['https:','http:'].includes(url.protocol))throw new Error('请使用 HTTP(S) 接口地址。');
      if(url.username||url.password)throw new Error('请在 API Key 输入框中填写凭据，不要将其放入接口地址。');
      if(!model.trim())throw new Error('自定义接口需要填写模型名称。');
      setSettings({endpoint:endpoint.trim(),model:model.trim()});
    }catch(caught){setFormError(errorText(caught));}
  }
  return <section className="panel settings-section ai-connection-settings">
    <div className="settings-heading"><span className="settings-icon"><Sparkles size={23}/></span><div><h2>AI 学习搭子</h2><p>查看服务器网关的配置、连接与能力状态，或使用自己的 AI 接口。</p></div><Badge tone={!unsaved&&activeMode==='server'&&configured&&!loading&&!loadError?'green':'neutral'}>{status}</Badge></div>
    <div className="connection-mode-tabs" role="tablist" aria-label="AI 连接方式">
      <button type="button" role="tab" aria-selected={mode==='server'} className={mode==='server'?'active':''} onClick={()=>{setMode('server');setFormError('');}}><Server size={16}/> 使用服务器网关</button>
      <button type="button" role="tab" aria-selected={mode==='custom'} className={mode==='custom'?'active':''} onClick={()=>{setMode('custom');setFormError('');}}>自定义接口</button>
    </div>
    <form onSubmit={event=>{event.preventDefault();save();}}>
      {mode==='server'?<>
        <div className="gateway-summary"><span className="gateway-logo"><Server size={24}/></span><div><strong>{loading?'正在读取…':gateway?.gatewayName||'服务器网关'}</strong><p>{loadError?'暂时无法确认服务器上的实际配置。':configured?'模型和鉴权由服务器统一配置，无需在网页中填写 Key。':'服务器尚未配置模型；基础学习功能不受影响。'}</p></div><Button type="button" variant="ghost" disabled={loading} onClick={()=>setRefresh(value=>value+1)}><RefreshCw size={14}/>刷新配置</Button></div>
        {loadError&&<div className="inline-error" role="alert">{loadError}</div>}
        {!loadError&&<div className="settings-form gateway-fields">
          <label>上游网关接口<input aria-label="上游网关接口" readOnly value={gateway?.endpoint||''} placeholder={loading?'正在读取…':'未配置'}/><small>这是网站服务器连接的实际网关地址；本机地址指运行网站的服务器。</small></label>
          <label>当前模型 / 路由组<input aria-label="当前模型 / 路由组" readOnly value={gateway?.model||''} placeholder={loading?'正在读取…':'未配置'}/><small>实际调用使用此服务器配置，自动切换由网关路由组负责。</small></label>
          <div className="gateway-auth"><span>鉴权状态</span><div><ShieldCheck size={17}/>{loading?'正在读取…':gateway?.authConfigured?'已在服务端配置':'上游未配置 API Key'}</div><small>网页不会读取或显示服务端密钥。</small></div>
        </div>}
        {!loadError&&<div className="status-triplet"><div className={`status-tile ${configured?'ok':'warn'}`}><span>配置状态</span><strong>{configured?'已配置':'未配置'}</strong></div><div className={`status-tile ${gateway?.connectionStatus==='ok'?'ok':gateway?.connectionStatus==='failed'?'bad':'warn'}`}><span>连接状态</span><strong>{connectionLabel(gateway?.connectionStatus)}</strong></div><div className={`status-tile ${gateway?.capabilityStatus==='verified'?'ok':gateway?.capabilityStatus==='failed'?'bad':'warn'}`}><span>能力验证</span><strong>{capabilityLabel(gateway?.capabilityStatus)}</strong></div></div>}
        {!loading&&!loadError&&!configured&&gateway?.unavailableReason&&<div className="inline-warning"><AlertTriangle size={15}/>AI 暂不可用：{gateway.unavailableReason}</div>}
        {!loading&&!loadError&&gateway?.teaching&&gateway.teaching.configured===false&&<div className="inline-warning"><AlertTriangle size={15}/>教学服务未配置{gateway.teaching.reason?`：${gateway.teaching.reason}`:''}。阅读、练习与写作仍可使用。</div>}
        {!loading&&!loadError&&gateway?.budget&&<p className="muted-text">今日调用 {gateway.budget.usedToday??0}{gateway.budget.dailyLimit?` / ${gateway.budget.dailyLimit}`:'（不限）'} 次 · 并发 {gateway.concurrency?.active??0}/{gateway.concurrency?.limit??'—'}</p>}
        <details className="technical-details gateway-connection-details"><summary>查看网站转发地址</summary><p>浏览器请求 <code>{websiteEndpoint}</code>，网站服务器再连接上方的网关。此地址是网站转发入口，模型与上游地址以服务器配置为准。</p></details>
      </>:<div className="settings-form">
        <label>完整接口地址<input aria-label="完整接口地址" type="url" required value={endpoint} onChange={event=>setEndpoint(event.target.value)} placeholder="https://…/v1/chat/completions"/><small>支持兼容 Chat Completions 且允许浏览器跨域的接口。</small></label>
        <label>模型名称<input aria-label="模型名称" required value={model} onChange={event=>setModel(event.target.value)} placeholder="填写服务商提供的模型 ID"/></label>
        <label>API Key<input aria-label="API Key" type="password" autoComplete="off" value={apiKey} onChange={event=>setApiKey(event.target.value)} placeholder="仅保留在当前页面，刷新后清除"/><small>不写入浏览器数据库或备份。</small></label>
      </div>}
      {formError&&<div className="inline-error" role="alert">{formError}</div>}
      <div className="settings-action"><p><ShieldCheck size={15}/>{unsaved?'保存后才会切换当前使用的连接方式。':mode==='server'?'当前使用服务器网关，显示的是服务器配置。':'当前使用你保存的自定义接口。'}</p><Button type="submit" disabled={mode==='server'&&!unsaved}>{mode==='server'?(unsaved?'使用此网关':'当前正在使用'):'保存自定义接口'}<CheckCircle2 size={16}/></Button></div>
    </form>
  </section>;
}
