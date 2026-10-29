import React,{Component,useEffect,useState,type ReactNode} from 'react';
import ReactDOM from 'react-dom/client';
import {createRootRoute,createRoute,createRouter,RouterProvider,Outlet,Link,useLocation,useNavigate} from '@tanstack/react-router';
import {ArrowUpRight,BookOpen,Check,ChevronRight,GraduationCap,KeyRound,LayoutDashboard,Layers,LogOut,Menu,Moon,PenLine,Search,Settings2,ShieldCheck,Sun,X} from 'lucide-react';
import {AuthProvider,useAuth} from './auth';
import {AppProvider,useApp} from './context';
import {AIPanel} from './components/AIPanel';
import {SyncBanner,SyncStatus} from './components/SyncStatus';
import {AuthScreens} from './pages/Auth';
import {Dashboard} from './pages/Dashboard';
import {Practice} from './pages/Practice';
import {Knowledge} from './pages/Knowledge';
import {Writing} from './pages/Writing';
import {Settings} from './pages/Settings';
import {Admin} from './pages/Admin';
import {Loading,Button} from './ui';
import './styles.css';
import 'katex/dist/katex.min.css';

class ErrorBoundary extends Component<{children:ReactNode},{error:string}>{state={error:''};static getDerivedStateFromError(error:Error){return {error:error.message};}render(){return this.state.error?<div className="fatal-error"><h1>学习室暂时无法打开</h1><p>{this.state.error}</p><p>请检查网络连接后重试。服务器上保存的学习数据不会丢失。</p><Button onClick={()=>window.location.reload()}>重新打开</Button></div>:this.props.children;}}

const nav=[{to:'/' as const,label:'学习概览',icon:LayoutDashboard},{to:'/practice' as const,label:'题库练习',icon:Layers},{to:'/knowledge' as const,label:'知识手册',icon:BookOpen},{to:'/cases' as const,label:'案例工坊',icon:GraduationCap},{to:'/essays' as const,label:'论文写作',icon:PenLine}];
const adminNav={to:'/admin' as const,label:'管理后台',icon:ShieldCheck};

function Root(){
  const auth=useAuth();
  const location=useLocation();
  if(auth.status==='loading') return <div className="boot-screen"><Loading/></div>;
  if(auth.status==='anonymous') return <AuthScreens/>;
  if(['/login','/setup','/invite'].includes(location.pathname)) return <HomeRedirect/>;
  return <AppProvider><Layout/></AppProvider>;
}

function HomeRedirect(){
  const navigate=useNavigate();
  useEffect(()=>{void navigate({to:'/',replace:true});},[navigate]);
  return <Loading/>;
}

function Layout(){
  const location=useLocation(),navigate=useNavigate();
  const {theme,toggleTheme,catalog}=useApp();
  const {user,capabilities,logout}=useAuth();
  const [mobileOpen,setMobileOpen]=useState(false);
  const items=capabilities.admin?[...nav,adminNav]:nav;
  const current=items.find(item=>item.to===location.pathname)?.label||'设置与数据';
  useEffect(()=>{setMobileOpen(false);window.scrollTo({top:0,behavior:'instant'});},[location.pathname]);
  useEffect(()=>{const handler=(event:KeyboardEvent)=>{if((event.metaKey||event.ctrlKey)&&event.key==='k'){event.preventDefault();void navigate({to:'/knowledge'});setTimeout(()=>document.querySelector<HTMLInputElement>('[aria-label="搜索知识全文"]')?.focus(),100);}};window.addEventListener('keydown',handler);return()=>window.removeEventListener('keydown',handler);},[navigate]);
  const displayName=user?.displayName||user?.email||'学习者';
  return <div className="app-shell">
    {mobileOpen&&<button className="mobile-backdrop" aria-label="关闭导航" onClick={()=>setMobileOpen(false)}/>}
    <aside className={`sidebar ${mobileOpen?'is-open':''}`}>
      <Link to="/" className="brand"><span className="brand-symbol"><Layers size={23}/></span><div><strong>RKA<span>学习室</span></strong><small>成为更好的架构师</small></div></Link>
      <div className="sidebar-section-label">学习空间</div>
      <nav aria-label="主导航">{items.map(item=><Link to={item.to} key={item.to} activeOptions={{exact:item.to==='/'}} activeProps={{className:'active'}}><item.icon size={19}/><span>{item.label}</span>{location.pathname===item.to&&<i/>}</Link>)}</nav>
      <div className="sidebar-study-note"><div className="tiny-caps">A LITTLE, EVERY DAY.</div><p>学有所思，<br/>练有所获。</p><div className="sidebar-note-line"/><span>{catalog.choices.length.toLocaleString()} 道题 · {(catalog.materials||[]).length} 份完整资料</span></div>
      <div className="sidebar-bottom"><Link to="/settings" activeProps={{className:'active'}}><Settings2 size={18}/>设置与数据</Link><div className="local-profile"><span className="profile-avatar">{displayName.slice(0,1)}</span><div><strong>{displayName}</strong><small><span/><SyncStatus compact/></small></div><Check size={15}/></div></div>
    </aside>
    <div className="app-body">
      <header className="topbar">
        <div className="breadcrumbs"><button className="icon-button mobile-menu" onClick={()=>setMobileOpen(!mobileOpen)} aria-label="打开导航">{mobileOpen?<X size={20}/>:<Menu size={20}/>}</button><span>学习空间</span><ChevronRight size={14}/><strong>{current}</strong></div>
        <div className="topbar-actions">
          <button className="global-search" onClick={()=>{void navigate({to:'/knowledge'});setTimeout(()=>document.querySelector<HTMLInputElement>('[aria-label="搜索知识全文"]')?.focus(),100);}}><Search size={16}/><span>搜索知识点</span><kbd>⌘ K</kbd></button>
          <span className="header-divider"/>
          <button className="icon-button" onClick={toggleTheme} aria-label={theme==='light'?'切换深色模式':'切换浅色模式'}>{theme==='light'?<Moon size={19}/>:<Sun size={19}/>}</button>
          <span className="top-avatar" title={`${displayName}（${user?.role==='admin'?'管理员':'学习者'}）`}>{displayName.slice(0,1)}</span>
          <button className="icon-button" onClick={()=>{void logout();}} aria-label="退出登录"><LogOut size={18}/></button>
        </div>
      </header>
      {user?.mustChangePassword&&<div className="must-change-banner" role="alert"><KeyRound size={16}/><span>当前使用的是初始密码，请先修改密码后再继续学习。</span><Link to="/settings">前往修改</Link></div>}
      <SyncBanner/>
      <div className="content-wrap"><Outlet/></div>
      <footer className="site-footer"><span>RKA · 系统架构设计师学习室</span><Link to="/settings">账号、数据与内容来源 <ArrowUpRight size={12}/></Link></footer>
    </div>
    <AIPanel/>
  </div>;
}

const rootRoute=createRootRoute({component:Root,notFoundComponent:()=> <div className="fatal-error"><h1>没有找到这个页面</h1><Link to="/">返回学习概览</Link></div>});
const validateSearch=(search:Record<string,unknown>)=>({...(typeof search.id==='string'?{id:search.id}:{}),...(typeof search.version==='string'?{version:search.version}:{}),...(typeof search.mode==='string'?{mode:search.mode}:{}),...(typeof search.module==='string'?{module:search.module}:{}),...(typeof search.material==='string'?{material:search.material}:{}),...(typeof search.section==='string'?{section:search.section}:{}),...(search.start==='1'?{start:'1'}:{}),...(search.from==='practice'?{from:'practice'}:{}),...(typeof search.token==='string'?{token:search.token}:{}),...(typeof search.next==='string'?{next:search.next}:{})});
const AuthRoute=()=>null;
const routeTree=rootRoute.addChildren([
  createRoute({getParentRoute:()=>rootRoute,path:'/',component:Dashboard}),
  createRoute({getParentRoute:()=>rootRoute,path:'/practice',validateSearch,component:Practice}),
  createRoute({getParentRoute:()=>rootRoute,path:'/knowledge',validateSearch,component:Knowledge}),
  createRoute({getParentRoute:()=>rootRoute,path:'/cases',validateSearch,component:()=> <Writing kind="case"/>}),
  createRoute({getParentRoute:()=>rootRoute,path:'/essays',validateSearch,component:()=> <Writing kind="essay"/>}),
  createRoute({getParentRoute:()=>rootRoute,path:'/settings',component:Settings}),
  createRoute({getParentRoute:()=>rootRoute,path:'/admin',component:Admin}),
  createRoute({getParentRoute:()=>rootRoute,path:'/login',validateSearch,component:AuthRoute}),
  createRoute({getParentRoute:()=>rootRoute,path:'/setup',component:AuthRoute}),
  createRoute({getParentRoute:()=>rootRoute,path:'/invite',validateSearch,component:AuthRoute}),
]);
const router=createRouter({routeTree,defaultPreload:'intent'});
declare module '@tanstack/react-router' {interface Register {router:typeof router}}
ReactDOM.createRoot(document.getElementById('root')!).render(<ErrorBoundary><AuthProvider><RouterProvider router={router}/></AuthProvider></ErrorBoundary>);
