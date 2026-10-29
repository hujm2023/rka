import {useEffect,useState,type CSSProperties} from 'react';
import {Link} from '@tanstack/react-router';
import {useLiveQuery} from 'dexie-react-hooks';
import {ArrowRight,BookOpen,Layers,PenLine,Sparkles,Target,Flame,Check,Play,Clock3,AlertTriangle} from 'lucide-react';
import {useApp} from '../context';
import {apiGet,errorText} from '../api';
import {cacheGet,cachePut} from '../sync';
import {db} from '../db';
import {latestAttempts,localDay,percent,isDue} from '../study';
import {modules,type ServerSession} from '../types';
import {Badge,Button} from '../ui';

const pickNumber=(source:Record<string,unknown>|null|undefined,keys:string[],fallback:number)=>{
  if(!source) return fallback;
  for(const key of keys){const value=source[key];if(typeof value==='number'&&Number.isFinite(value)) return value;}
  return fallback;
};
const pickArray=(source:Record<string,unknown>|null|undefined,keys:string[]):Record<string,unknown>[]=>{
  if(!source) return [];
  for(const key of keys){const value=source[key];if(Array.isArray(value)) return value.filter((item):item is Record<string,unknown>=>!!item&&typeof item==='object');}
  return [];
};

export function Dashboard(){
  const {catalog,attempts,reading,stats,summary,statsError,summaryError,refreshStats}=useApp();
  const [session,setSession]=useState<ServerSession|null>(null);
  const drafts=useLiveQuery(()=>db.drafts.toArray(),[],[])||[];
  useEffect(()=>{
    let alive=true;
    apiGet<unknown>('/api/practice/sessions/active').then(async raw=>{
      const data=raw as {session?:unknown};
      const normalizedSession=data?.session?data.session:null;
      if(!alive) return;
      setSession(normalizedSession?normalizedSession as ServerSession:null);
      await cachePut('active-session',normalizedSession);
    }).catch(async()=>{
      const cached=await cacheGet<ServerSession|null>('active-session');
      if(alive&&cached) setSession(cached.value||null);
    });
    return()=>{alive=false;};
  },[]);
  const latest=latestAttempts(attempts),today=localDay();
  const localToday=attempts.filter(item=>localDay(new Date(item.createdAt))===today);
  const totals=(stats?.totals&&typeof stats.totals==='object'?stats.totals:{}) as Record<string,unknown>;
  const recent=pickArray(stats,['activity','recentActivity']);
  const todayActivity=recent.find(item=>String(item.date||item.day||'').slice(0,10)===today);
  const todayCount=todayActivity?Number(todayActivity.attempts||todayActivity.count||0):new Set(localToday.map(item=>item.questionId)).size;
  const practiced=pickNumber(totals,['distinctQuestions','practicedQuestions'],pickNumber(stats,['practicedQuestions'],latest.size));
  const totalAttempts=pickNumber(totals,['attempts'],attempts.length);
  const independentCorrect=pickNumber(totals,['independentCorrect'],attempts.filter(item=>item.correct&&!item.assisted).length);
  const correctRate=totalAttempts?Math.round(independentCorrect/totalAttempts*100):pickNumber(totals,['accuracyPercent','accuracy'],0);
  const wrong=pickNumber(totals,['wrongQuestions','wrongCount'],[...latest.values()].filter(item=>!item.correct).length);
  const due=pickNumber(totals,['dueQuestions','dueCount'],[...latest.values()].filter(item=>isDue(item)).length);
  const summaryCounts=(summary?.counts||{}) as Record<string,unknown>;
  const readingCount=pickNumber(summaryCounts,['reading'],pickNumber(summary,['reading','readingCount'],reading.filter(item=>item.done).length));
  const totalMaterials=(catalog.materials||[]).length;
  const lastReading=reading.slice().sort((a,b)=>b.updatedAt.localeCompare(a.updatedAt))[0];
  const lastLesson=(catalog.materials||[]).find(material=>material.id===lastReading?.id);
  const week=Array.from({length:7},(_,index)=>{
    const date=new Date();date.setDate(date.getDate()-(6-index));
    const day=localDay(date);
    const activity=recent.find(item=>String(item.date||item.day||'').slice(0,10)===day);
    return {day:date.toLocaleDateString('zh-CN',{weekday:'short'}).replace('周',''),count:activity?Number(activity.attempts||activity.count||0):attempts.filter(item=>localDay(new Date(item.createdAt))===day).length,date:day};
  });
  const domainRows:Record<string,unknown>[]=pickArray(stats,['domains','modules']);
  const domains:Record<string,unknown>[]=domainRows.length?domainRows:['architecture','software_engineering','database','computer_foundation'].map(key=>{
    const questions=catalog.choices.filter(question=>question.module===key);
    const answered=questions.filter(question=>latest.has(question.id)).length;
    return {module:key,total:questions.length,practiced:answered};
  });
  const maxWeek=Math.max(20,...week.map(item=>item.count));
  return <div className="dashboard page-enter">
    <div className="dashboard-greeting"><div><span className="eyebrow">YOUR LEARNING JOURNEY</span><h1>每一次理解，<br className="mobile-break"/>都离架构师更近一步。</h1><p>把知识连成体系，让练习成为积累。</p></div><div className="date-chip"><span>{new Date().toLocaleDateString('zh-CN',{month:'long',day:'numeric'})}</span><small>{new Date().toLocaleDateString('zh-CN',{weekday:'long'})}</small></div></div>
    {(statsError||summaryError)&&<div className="inline-error dashboard-data-warning" role="alert"><AlertTriangle size={15}/><span>{statsError||summaryError}</span><Button variant="secondary" onClick={refreshStats}>重新读取</Button></div>}
    <div className="hero-grid"><section className="learning-hero"><div className="hero-copy"><Badge tone="hero">系统架构设计师 · 高级</Badge><h2>{session&&!session.finished?'从上次停下的地方，继续。':'今天，也向前一小步。'}</h2><p>{session&&!session.finished?`你正在练习第 ${session.index+1} / ${session.questionIds.length} 题，学习现场保存在服务器上。`:'从一组 20 题开始，找到薄弱点，再把它变成你的得分点。'}</p><Button asChild className="hero-button"><Link to="/practice"><Play size={16} fill="currentColor"/>{session&&!session.finished?'继续上次练习':'开始今日练习'}<ArrowRight size={17}/></Link></Button><div className="hero-foot"><span className="small-dot"/>进度已同步到服务器，可跨设备继续</div></div><div className="architecture-art" aria-hidden="true"><div className="art-orbit orbit-one"/><div className="art-orbit orbit-two"/><div className="art-grid"/><div className="art-block block-back"><Layers size={34}/></div><div className="art-block block-front"><span>LEARN</span><strong>构建<br/>知识体系</strong><div className="art-line"><i/><i/><i/></div></div><div className="art-dot dot-one"/><div className="art-dot dot-two"/><span className="art-label">UNDERSTAND. CONNECT. BUILD.</span></div></section>
    <section className="daily-card"><div className="section-caption"><span><Target size={17}/> 今日练习</span><Badge>20 题目标</Badge></div><div className="daily-ring" style={{'--progress':`${Math.min(100,todayCount/20*100)}%`} as CSSProperties}><div><strong>{todayCount}<span>/20</span></strong><small>{todayCount>=20?'今日目标已完成':'积累从这一题开始'}</small></div></div><div className="daily-bottom"><Flame size={16}/>{todayCount>=20?'很棒！再回顾一下今天的错题。':`再完成 ${Math.max(0,20-todayCount)} 题，达成今日目标`}</div></section></div>
    <div className="stat-grid">{[{label:'已练习题目',value:practiced,suffix:`/ ${catalog.choices.length||'—'}`,icon:Layers,note:'来自服务器作答记录'},{label:'独立答对比例',value:correctRate,suffix:'%',icon:Target,note:'独立答对次数 / 总作答次数'},{label:'待巩固错题',value:wrong,suffix:'题',icon:BookOpen,note:`其中 ${due} 题已到复习时间`},{label:'已学完整资料',value:readingCount,suffix:`/ ${totalMaterials||'—'}`,icon:Check,note:'阅读进度报告给服务器'}].map(stat=><section className="stat-card" key={stat.label}><div><span>{stat.label}</span><stat.icon size={17}/></div><strong>{stat.value}<small>{stat.suffix}</small></strong><p>{stat.note}</p></section>)}</div>
    <div className="dashboard-lower"><section className="panel learning-path"><div className="section-heading"><div><h2>你的学习路径</h2><p>综合知识、案例分析、论文，稳步推进。</p></div><span className="tiny-caps">STUDY PATH</span></div><Link to="/knowledge" search={lastLesson?{id:lastLesson.id}:{}} className="path-row"><span className="path-number">01</span><div className="path-icon sage"><BookOpen size={23}/></div><div><h3>构建知识体系 <Badge tone="green">知识学习</Badge></h3><p>{lastLesson?`继续阅读 · ${lastLesson.title}`:'从架构风格、质量属性与设计模式开始，建立全局视野。'}</p></div><ArrowRight size={19}/></Link><Link to="/cases" className="path-row"><span className="path-number">02</span><div className="path-icon sand"><Layers size={23}/></div><div><h3>把知识用在案例里</h3><p>{catalog.cases.length} 道案例 · 分问作答、对照要点、AI 点评。</p></div><ArrowRight size={19}/></Link><Link to="/essays" className="path-row"><span className="path-number">03</span><div className="path-icon lavender"><PenLine size={23}/></div><div><h3>写好你的架构故事</h3><p>{drafts.filter(draft=>draft.kind==='essay').length?`已保存 ${drafts.filter(draft=>draft.kind==='essay').length} 篇草稿，继续打磨项目实践。`:'从项目素材到论文提纲，把你的技术思考写清楚。'}</p></div><ArrowRight size={19}/></Link></section>
    <section className="panel activity-card"><div className="section-heading"><h2>最近 7 天</h2><Clock3 size={17}/></div><div className="week-chart" aria-label="最近七天每日作答次数">{week.map(day=><div key={day.date}><span>{day.count||'·'}</span><div className="chart-track"><i style={{height:`${day.count?Math.max(8,day.count/maxWeek*100):3}%`}}/></div><small>{day.day}</small></div>)}</div><p>{attempts.length||stats?'节奏比速度更重要，保持自己的步调。':'还没有练习记录，今天就是起点。'}</p><Link to="/practice" search={{mode:'wrong'}} className="text-link">去巩固错题 <ArrowRight size={14}/></Link></section></div>
    <section className="panel module-overview"><div className="section-heading"><div><h2>知识领域</h2><p>看见进展，也看见下一步。</p></div><Link to="/practice" className="text-link">全部章节 <ArrowRight size={14}/></Link></div><div className="module-grid">{domains.slice(0,4).map((domain,index)=>{const key=String(domain.module||domain.key||['architecture','software_engineering','database','computer_foundation'][index]||'other');const total=Number(domain.totalQuestions||domain.total||catalog.choices.filter(question=>question.module===key).length);const done=Number(domain.attemptedQuestions||domain.practiced||domain.answered||0);return <Link to="/practice" search={{module:key}} key={key}><div><span>{modules[key]||key}</span><small>{done}/{total}</small></div><div className="progress-track"><i style={{width:`${percent(done,Math.max(1,total))}%`}}/></div></Link>;})}</div></section>
    <div className="dashboard-note"><Sparkles size={16}/><span>遇到卡点时，打开题目旁的「AI 辅导」，一起理清思路。</span><Link to="/settings">配置学习搭子 <ArrowRight size={14}/></Link></div>
  </div>;
}
