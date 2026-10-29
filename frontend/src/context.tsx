import {createContext,useCallback,useContext,useEffect,useLayoutEffect,useState,type ReactNode} from 'react';
import {useLiveQuery} from 'dexie-react-hooks';
import {useAuth} from './auth';
import {accountOwner} from './account';
import {acceptReadingSummary} from './reading';
import {apiGet,errorText,normalizeCatalog,normalizeMarks} from './api';
import {db} from './db';
import {startOutbox} from './outbox';
import {cacheGet,cachePut} from './sync';
import type {AIContext,AISettings,Attempt,Catalog,LearningSummary,Mark,PracticeStats,Reading} from './types';

const emptyCatalog:Catalog={version:'',choices:[],cases:[],essays:[],lessons:[],materials:[],knowledgeLinks:{}};

type AppContextValue={
  catalog:Catalog; catalogReady:boolean; catalogSource:'server'|'cache'|'empty'; catalogError:string;
  reloadCatalog:()=>void;
  attempts:Attempt[]; marks:Mark[]; reading:Reading[]; refreshMarks:()=>void;
  stats:PracticeStats|null; summary:LearningSummary|null; statsError:string; summaryError:string; refreshStats:()=>void;
  ai:AIContext|null; setAI:(ctx:AIContext|null)=>void;
  settings:AISettings; setSettings:(value:AISettings)=>void;
  apiKey:string; setApiKey:(value:string)=>void;
  notify:(text:string)=>void;
  theme:string; toggleTheme:()=>void;
};

const Context=createContext<AppContextValue|null>(null);
export const useApp=()=>{const value=useContext(Context);if(!value)throw new Error('Missing app context');return value;};

const settingsKey=()=>`rka-ai-settings:${accountOwner()||'unassigned'}`;
function savedSettings():AISettings{try{const value=JSON.parse(localStorage.getItem(settingsKey())||'null');if(value&&typeof value.endpoint==='string'&&typeof value.model==='string')return value;}catch{/* use defaults */}return {endpoint:'/api/ai',model:''};}

export function AppProvider({children}:{children:ReactNode}){
  useAuth();
  const attempts=useLiveQuery(()=>db.attempts.toArray(),[],[])||[];
  const reading=useLiveQuery(()=>db.reading.toArray(),[],[])||[];
  const [catalog,setCatalog]=useState<Catalog>(emptyCatalog);
  const [catalogReady,setCatalogReady]=useState(false);
  const [catalogSource,setCatalogSource]=useState<'server'|'cache'|'empty'>('empty');
  const [catalogError,setCatalogError]=useState('');
  const [marks,setMarks]=useState<Mark[]>([]);
  const [stats,setStats]=useState<PracticeStats|null>(null);
  const [summary,setSummary]=useState<LearningSummary|null>(null);
  const [statsError,setStatsError]=useState('');
  const [summaryError,setSummaryError]=useState('');
  const [ai,setAI]=useState<AIContext|null>(null);
  const [settings,updateSettings]=useState(savedSettings);
  const [apiKey,setApiKey]=useState('');
  const [toast,setToast]=useState('');
  const [error,setError]=useState('');
  const [theme,setTheme]=useState(()=>{try{return localStorage.getItem('rka-theme')==='dark'?'dark':'light';}catch{return 'light';}});

  useLayoutEffect(()=>{document.documentElement.dataset.theme=theme;document.querySelector('meta[name="theme-color"]')?.setAttribute('content',theme==='dark'?'#171e1b':'#f6f7f5');try{localStorage.setItem('rka-theme',theme);}catch{setError('浏览器禁止保存偏好设置。');}},[theme]);
  useEffect(()=>{if(!toast)return;const timer=setTimeout(()=>setToast(''),4500);return()=>clearTimeout(timer);},[toast]);
  useEffect(()=>{const listener=(event:Event)=>setError(`操作未完成：${(event as CustomEvent).detail}。已保存的服务器数据不受影响。`);window.addEventListener('rka-error',listener);return()=>window.removeEventListener('rka-error',listener);},[]);
  useEffect(()=>startOutbox(),[]);

  const reloadCatalog=useCallback(async()=>{
    setCatalogError('');
    try{
      const raw=await apiGet<unknown>('/api/content/catalog');
      const next=normalizeCatalog(raw);
      setCatalog(next);setCatalogReady(true);setCatalogSource('server');
      await cachePut('catalog',next);
    }catch(caught){
      const cached=await cacheGet<Catalog>('catalog');
      if(cached){setCatalog(cached.value);setCatalogReady(true);setCatalogSource('cache');setCatalogError(`正在使用离线缓存的内容（${errorText(caught)}）`);}
      else{setCatalogReady(true);setCatalogSource('empty');setCatalogError(errorText(caught));}
    }
  },[]);
  useEffect(()=>{void reloadCatalog();},[reloadCatalog]);

  const refreshMarks=useCallback(async()=>{
    try{
      const data=await apiGet<unknown>('/api/practice/marks');
      const next=normalizeMarks(data);
      setMarks(next);await cachePut('marks',next);
    }catch(caught){
      const cached=await cacheGet<Mark[]>('marks');
      if(cached)setMarks(cached.value);
      if(!cached)setMarks([]);
    }
  },[]);
  useEffect(()=>{void refreshMarks();},[refreshMarks]);

  const refreshStats=useCallback(async()=>{
    try{
      const data=await apiGet<PracticeStats>('/api/practice/stats');
      setStats(data);setStatsError('');await cachePut('stats',data);
    }catch(caught){
      const cached=await cacheGet<PracticeStats>('stats');
      if(cached){setStats(cached.value);setStatsError(`统计显示离线缓存：${errorText(caught)}`);}
      else setStatsError(errorText(caught));
    }
    try{
      const data=await apiGet<LearningSummary>('/api/learning/summary');
      setSummary(data);setSummaryError('');await acceptReadingSummary(data);await cachePut('summary',data);
    }catch(caught){
      const cached=await cacheGet<LearningSummary>('summary');
      if(cached){setSummary(cached.value);setSummaryError(`学习概览显示离线缓存：${errorText(caught)}`);}
      else setSummaryError(errorText(caught));
    }
  },[]);
  useEffect(()=>{void refreshStats();},[refreshStats]);

  const setSettings=(value:AISettings)=>{try{localStorage.setItem(settingsKey(),JSON.stringify(value));updateSettings(value);setToast('AI 接口设置已保存；密钥只保留在当前页面内存。');}catch{setError('无法保存接口设置。');}};

  return <Context.Provider value={{catalog,catalogReady,catalogSource,catalogError,reloadCatalog,attempts,marks,reading,refreshMarks,stats,summary,statsError,summaryError,refreshStats,ai,setAI,settings,setSettings,apiKey,setApiKey,notify:setToast,theme,toggleTheme:()=>setTheme(theme==='light'?'dark':'light')}}>{children}{toast&&<div className="toast" role="status">{toast}</div>}{error&&<div className="error-banner" role="alert">{error}<button onClick={()=>setError('')}>关闭</button></div>}</Context.Provider>;
}
