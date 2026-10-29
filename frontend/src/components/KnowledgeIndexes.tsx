import {useEffect,useState} from 'react';
import {apiGet,apiPost,errorText} from '../api';
import {Badge,Button} from '../ui';

type Index={id:string;materialId:string;versionId:string;title:string;status:string;error:string};
export function KnowledgeIndexes({materialId,admin=false}:{materialId?:string;admin?:boolean}){
  const [items,setItems]=useState<Index[]>([]),[configured,setConfigured]=useState(false),[error,setError]=useState('');
  const load=async()=>{try{const data=await apiGet<{items:Index[];configured:boolean}>('/api/content/indexes');setItems(data.items);setConfigured(data.configured);setError('');}catch(e){setError(errorText(e));}};
  useEffect(()=>{void load();const timer=setInterval(()=>void load(),15000);return()=>clearInterval(timer);},[]);
  if(!admin){
    const relevant=items.filter(item=>item.materialId===materialId);
    const ready=relevant.some(item=>item.status==='ready');
    return <p className="muted-text" role="status">{ready?'AI 已可检索本章原文':!configured?'AI 检索尚未配置':relevant.some(item=>item.status==='failed')?'本章 AI 检索暂不可用，请联系管理员重试':'AI 正在准备本章资料'} · 原文阅读与章节练习可正常使用。</p>;
  }
  return <section className="panel"><div className="section-heading"><h3>知识检索索引</h3><Button variant="secondary" onClick={()=>void load()}>刷新索引状态</Button></div><p className="muted-text">原文解析与 AI 检索分别验收。新索引通过验证后启用，已有版本继续保留。</p>{error&&<p role="alert">{error}</p>}<div className="table-wrap"><table><thead><tr><th>资料</th><th>检索状态</th><th>说明</th><th>操作</th></tr></thead><tbody>{items.map(item=><tr key={item.id}><td>{item.title}</td><td><Badge>{({ready:'已验证',indexing:'构建中',queued:'待处理',failed:'失败'} as Record<string,string>)[item.status]||item.status}</Badge></td><td>{item.error||'—'}</td><td>{item.status==='failed'&&<Button variant="secondary" onClick={()=>void apiPost(`/api/admin/content/indexes/${item.id}/retry`).then(load).catch(e=>setError(errorText(e)))}>重建索引</Button>}</td></tr>)}</tbody></table></div>{!items.length&&<p>尚无检索索引。{configured?'后台会自动处理已解析的资料。':'请先配置教学服务。'}</p>}</section>;
}
