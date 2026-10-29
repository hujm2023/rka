import {useEffect,useState,type FormEvent,type ReactNode} from 'react';
import {Link,useLocation,useNavigate} from '@tanstack/react-router';
import {ArrowRight,GraduationCap,KeyRound,ShieldCheck,Sparkles} from 'lucide-react';
import {useAuth} from '../auth';
import {errorText} from '../api';
import {Button} from '../ui';

function AuthLayout({title,subtitle,children,footer}:{title:string;subtitle:string;children:ReactNode;footer?:ReactNode}){
  return <div className="auth-shell">
    <div className="auth-aside" aria-hidden="true">
      <div className="auth-brand"><span className="brand-symbol"><GraduationCap size={26}/></span><div><strong>RKA<span>学习室</span></strong><small>系统架构设计师 · 学习平台</small></div></div>
      <div className="auth-quote"><span className="tiny-caps">A LITTLE, EVERY DAY.</span><h2>每一次理解，<br/>都离架构师更近一步。</h2><p>登录后，作答、笔记、草稿与阅读进度会在你的设备之间保持一致。</p></div>
      <div className="auth-points"><span><ShieldCheck size={16}/>服务器保存学习记录</span><span><Sparkles size={16}/>AI 辅导与原文引用</span><span><KeyRound size={16}/>账号与设备由你管理</span></div>
    </div>
    <main className="auth-main"><section className="panel auth-card">
      <div className="auth-card-brand"><span className="brand-symbol"><GraduationCap size={23}/></span><div><strong>RKA<span>学习室</span></strong></div></div>
      <h1>{title}</h1><p className="auth-subtitle">{subtitle}</p>
      {children}
      {footer&&<div className="auth-footer">{footer}</div>}
    </section>
    <p className="auth-note">账号用于区分个人学习数据；服务器不会把密码或凭据发送给浏览器脚本。</p></main>
  </div>;
}

export function AuthScreens(){
  const location=useLocation();
  const search=location.search as Record<string,string|undefined>;
  if(location.pathname==='/setup') return <BootstrapPage/>;
  if(location.pathname==='/invite') return <InvitePage initialToken={search.token||''}/>;
  return <LoginPage next={search.next}/>;
}

function LoginPage({next}:{next?:string}){
  const {login}=useAuth();const navigate=useNavigate();
  const [email,setEmail]=useState(''),[password,setPassword]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  async function submit(event:FormEvent){
    event.preventDefault();setError('');setBusy(true);
    try{
      await login(email.trim(),password);
      const target=next&&next.startsWith('/')?next:'/';
      window.history.replaceState(null,'',target);
      await navigate({to:target,replace:true});
    }catch(caught){setError(errorText(caught));}
    finally{setBusy(false);}
  }
  return <AuthLayout title="登录学习空间" subtitle="使用你的账号继续上次的阅读、练习与写作。"
    footer={<div className="auth-links"><Link to="/setup">首次部署？初始化管理员</Link><Link to="/invite">使用邀请链接</Link></div>}>
    <form className="auth-form" onSubmit={submit}>
      <label>邮箱<input aria-label="邮箱" type="email" autoComplete="username" required value={email} onChange={event=>setEmail(event.target.value)} placeholder="you@example.com"/></label>
      <label>密码<input aria-label="密码" type="password" autoComplete="current-password" required value={password} onChange={event=>setPassword(event.target.value)} placeholder="请输入密码"/></label>
      {error&&<div className="inline-error" role="alert">{error}</div>}
      <Button type="submit" disabled={busy}>{busy?'正在登录…':'登录'}<ArrowRight size={16}/></Button>
    </form>
  </AuthLayout>;
}

function BootstrapPage(){
  const {bootstrap}=useAuth();const navigate=useNavigate();
  const [token,setToken]=useState(''),[email,setEmail]=useState(''),[displayName,setDisplayName]=useState(''),[password,setPassword]=useState(''),[confirm,setConfirm]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  async function submit(event:FormEvent){
    event.preventDefault();setError('');
    if(password!==confirm){setError('两次输入的密码不一致。');return;}
    setBusy(true);
    try{await bootstrap({token:token.trim(),email:email.trim(),displayName:displayName.trim()||email.split('@')[0],password});await navigate({to:'/',replace:true});}
    catch(caught){setError(errorText(caught));}
    finally{setBusy(false);}
  }
  return <AuthLayout title="建立首个管理员" subtitle="这是一次性初始化入口。令牌只保存在服务器 data/secrets/bootstrap-token 中。"
    footer={<div className="auth-links"><Link to="/login">返回登录</Link></div>}>
    <form className="auth-form" onSubmit={submit}>
      <label>初始化令牌<input aria-label="初始化令牌" required value={token} onChange={event=>setToken(event.target.value)} placeholder="粘贴服务器 data/secrets/bootstrap-token 的内容"/><small>令牌位于服务器数据目录，不会通过网页自动读取。</small></label>
      <label>邮箱<input aria-label="管理员邮箱" type="email" required value={email} onChange={event=>setEmail(event.target.value)} placeholder="admin@example.com"/></label>
      <label>显示名称<input aria-label="显示名称" value={displayName} onChange={event=>setDisplayName(event.target.value)} placeholder="可选"/></label>
      <label>密码<input aria-label="设置密码" type="password" autoComplete="new-password" required value={password} onChange={event=>setPassword(event.target.value)}/><small>至少 10 位，包含字母与数字。</small></label>
      <label>确认密码<input aria-label="确认密码" type="password" autoComplete="new-password" required value={confirm} onChange={event=>setConfirm(event.target.value)}/></label>
      {error&&<div className="inline-error" role="alert">{error}</div>}
      <Button type="submit" disabled={busy}>{busy?'正在建立…':'建立管理员并进入学习室'}<ArrowRight size={16}/></Button>
    </form>
  </AuthLayout>;
}

function InvitePage({initialToken}:{initialToken:string}){
  const {acceptInvite}=useAuth();const navigate=useNavigate();
  const [token,setToken]=useState(initialToken),[displayName,setDisplayName]=useState(''),[password,setPassword]=useState(''),[confirm,setConfirm]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  useEffect(()=>{setToken(initialToken);},[initialToken]);
  async function submit(event:FormEvent){
    event.preventDefault();setError('');
    if(password!==confirm){setError('两次输入的密码不一致。');return;}
    setBusy(true);
    try{await acceptInvite({token:token.trim(),displayName:displayName.trim(),password});await navigate({to:'/',replace:true});}
    catch(caught){setError(errorText(caught));}
    finally{setBusy(false);}
  }
  return <AuthLayout title="接受邀请" subtitle="设置密码后，邀请对应的学习空间会立即开通。"
    footer={<div className="auth-links"><Link to="/login">已有账号？返回登录</Link></div>}>
    <form className="auth-form" onSubmit={submit}>
      <label>邀请令牌<input aria-label="邀请令牌" required value={token} onChange={event=>setToken(event.target.value)} placeholder="粘贴管理员发送的邀请令牌"/></label>
      <label>显示名称<input aria-label="显示名称" value={displayName} onChange={event=>setDisplayName(event.target.value)} placeholder="可选"/></label>
      <label>密码<input aria-label="设置密码" type="password" autoComplete="new-password" required value={password} onChange={event=>setPassword(event.target.value)}/><small>至少 10 位，包含字母与数字。</small></label>
      <label>确认密码<input aria-label="确认密码" type="password" autoComplete="new-password" required value={confirm} onChange={event=>setConfirm(event.target.value)}/></label>
      {error&&<div className="inline-error" role="alert">{error}</div>}
      <Button type="submit" disabled={busy}>{busy?'正在开通…':'接受邀请并登录'}<ArrowRight size={16}/></Button>
    </form>
  </AuthLayout>;
}
