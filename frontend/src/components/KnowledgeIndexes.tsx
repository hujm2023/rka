import {useEffect,useState} from 'react';
import {Link} from '@tanstack/react-router';
import {BookOpen,CheckCircle2,ChevronLeft,ChevronRight,FileText,RefreshCw,Search} from 'lucide-react';
import {apiGet,apiPost,errorText} from '../api';
import {Badge,Button} from '../ui';

type Index={id:string;materialId:string;versionId:string;title:string;status:string;error:string};
const statusLabels:Record<string,string>={ready:'已就绪',indexing:'构建中',queued:'待处理',failed:'需重试'};
const pageSize=12;
export function KnowledgeIndexes({materialId,admin=false}:{materialId?:string;admin?:boolean}){
  const [items,setItems]=useState<Index[]>([]),[configured,setConfigured]=useState(false),[error,setError]=useState('');
  const [query,setQuery]=useState(''),[filter,setFilter]=useState('all'),[page,setPage]=useState(0),[loading,setLoading]=useState(true);
  const load=async()=>{setLoading(true);try{const data=await apiGet<{items:Index[];configured:boolean}>('/api/content/indexes');setItems(data.items);setConfigured(data.configured);setError('');}catch(e){setError(errorText(e));}finally{setLoading(false);}};
  useEffect(()=>{void load();const timer=setInterval(()=>void load(),15000);return()=>clearInterval(timer);},[]);
  if(!admin){
    const relevant=items.filter(item=>item.materialId===materialId);
    const ready=relevant.some(item=>item.status==='ready');
    return <p className="muted-text" role="status">{ready?'AI 已可检索本章原文':!configured?'AI 检索尚未配置':relevant.some(item=>item.status==='failed')?'本章 AI 检索暂不可用，请联系管理员重试':'AI 正在准备本章资料'} · 原文阅读与章节练习可正常使用。</p>;
  }
  const ready=items.filter(item=>item.status==='ready').length;
  const failed=items.filter(item=>item.status==='failed').length;
  const pending=items.filter(item=>item.status==='queued'||item.status==='indexing').length;
  const filtered=items.filter(item=>item.title.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())&&(filter==='all'||filter===item.status||(filter==='processing'&&(item.status==='queued'||item.status==='indexing')))).sort((a,b)=>a.title.localeCompare(b.title,'zh-CN',{numeric:true}));
  const pages=Math.max(1,Math.ceil(filtered.length/pageSize));
  const current=Math.min(page,pages-1);
  const visible=filtered.slice(current*pageSize,(current+1)*pageSize);
  return <section className="panel knowledge-index-panel">
    <div className="index-heading"><div className="index-heading-copy"><span className="admin-section-icon"><BookOpen size={20}/></span><div><h2>知识检索索引</h2><p>管理学习资料的 AI 检索状态，新版本验证通过后自动启用。</p></div></div><Button variant="secondary" disabled={loading} onClick={()=>void load()}><RefreshCw size={15} className={loading?'spin':''}/>刷新索引状态</Button></div>
    <div className="index-summary"><span className="index-summary-total"><strong>{items.length}</strong> 份资料</span><span className="index-summary-ready"><i/>{ready} 已就绪</span><span><i/>{pending} 处理中</span><span className={failed?'index-summary-warning':''}><i/>{failed} 需重试</span><div className="index-readiness" aria-label={`${ready} / ${items.length} 份资料已就绪`}><span style={{width:`${items.length?ready/items.length*100:0}%`}}/></div></div>
    {error&&<div className="inline-error" role="alert">{error}</div>}
    <div className="index-toolbar"><div className="search-field"><Search size={16}/><input aria-label="搜索索引资料" placeholder="搜索资料名称…" value={query} onChange={event=>{setQuery(event.target.value);setPage(0);}}/></div><select aria-label="筛选索引状态" value={filter} onChange={event=>{setFilter(event.target.value);setPage(0);}}><option value="all">全部状态</option><option value="ready">已就绪</option><option value="processing">处理中</option><option value="failed">需重试</option></select></div>
    <div className="admin-table-wrap"><table className="admin-table index-table"><colgroup><col className="index-name-col"/><col className="index-status-col"/><col className="index-note-col"/><col className="index-action-col"/></colgroup><thead><tr><th>资料名称</th><th>检索状态</th><th>状态说明</th><th>操作</th></tr></thead><tbody>{visible.map(item=><tr key={item.id}><td><div className="index-document"><span className="index-file-icon"><FileText size={17}/></span><strong>{item.title}</strong></div></td><td><Badge tone={item.status==='ready'?'green':item.status==='failed'?'amber':'neutral'}>{item.status==='ready'&&<CheckCircle2 size={12}/>} {statusLabels[item.status]||item.status}</Badge></td><td><span className={item.error?'index-error':'index-note'}>{item.error||(item.status==='ready'?'已验证，可用于 AI 检索':item.status==='queued'?'等待后台处理':item.status==='indexing'?'正在建立检索索引':'等待处理')}</span></td><td><div className="index-row-actions">{item.status==='failed'&&<Button variant="secondary" onClick={()=>void apiPost(`/api/admin/content/indexes/${item.id}/retry`).then(load).catch(e=>setError(errorText(e)))}>重建索引</Button>}<Link className="text-link" to="/knowledge" search={{id:item.materialId,version:item.versionId}}>查看原文 <ChevronRight size={14}/></Link></div></td></tr>)}</tbody></table>
      {!visible.length&&<div className="index-empty">{loading?'正在读取索引状态…':items.length?'没有匹配的资料，试试其他关键词或状态。':configured?'后台会自动处理已解析的资料。':'请先配置教学服务。'}</div>}
    </div>
    <div className="index-pagination"><span>{filtered.length?`${current*pageSize+1}–${Math.min((current+1)*pageSize,filtered.length)} / ${filtered.length} 条资料`:'0 条资料'}</span><div><Button variant="secondary" aria-label="索引上一页" disabled={current===0} onClick={()=>setPage(current-1)}><ChevronLeft size={15}/></Button><span>{current+1} / {pages}</span><Button variant="secondary" aria-label="索引下一页" disabled={current>=pages-1} onClick={()=>setPage(current+1)}><ChevronRight size={15}/></Button></div></div>
  </section>;
}
