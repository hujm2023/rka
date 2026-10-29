import {useEffect,useState} from 'react';
import {useLiveQuery} from 'dexie-react-hooks';
import {AlertTriangle,Cloud,CloudOff,RefreshCw} from 'lucide-react';
import {discardOutbox,flushOutbox,retryOutbox} from '../outbox';
import {overallStatus,syncDb,syncStatusText,type SyncStatus as SyncState} from '../sync';

function useOutboxRows(){
  const rows=useLiveQuery(()=>syncDb.outbox.toArray(),[],[])||[];
  return rows;
}

export function SyncStatus({compact=false}:{compact?:boolean}){
  const rows=useOutboxRows();
  const status: SyncState=overallStatus(rows);
  const pending=rows.filter(row=>row.state==='local'||row.state==='pending').length;
  const conflicts=rows.filter(row=>row.state==='conflict').length;
  return <span className={`save-indicator sync-indicator ${status}`} role="status" aria-label={`同步状态：${syncStatusText(status)}`}>
    <i/>
    {compact?syncStatusText(status):<>{syncStatusText(status)}{pending>0?`（${pending} 项）`:''}{conflicts>0?`（${conflicts} 项冲突）`:''}</>}
  </span>;
}

export function SyncBanner(){
  const rows=useOutboxRows();
  const [online,setOnline]=useState(()=>typeof navigator==='undefined'?true:navigator.onLine);
  const [busy,setBusy]=useState(false);
  useEffect(()=>{
    const up=()=>{setOnline(true);void flushOutbox();};
    const down=()=>setOnline(false);
    window.addEventListener('online',up);window.addEventListener('offline',down);
    return()=>{window.removeEventListener('online',up);window.removeEventListener('offline',down);};
  },[]);
  const pending=rows.filter(row=>row.state==='local'||row.state==='pending');
  const conflicts=rows.filter(row=>row.state==='conflict');
  if(online&&!pending.length&&!conflicts.length) return null;
  return <div className={`sync-banner ${conflicts.length?'conflict':online?'pending':'offline'}`} role="status">
    {online?conflicts.length?<AlertTriangle size={16}/>:<Cloud size={16}/>:<CloudOff size={16}/>}
    <div>
      {!online&&<span>网络不可用：更改会先保存在本机，恢复网络后自动同步。</span>}
      {online&&pending.length>0&&<span>{pending.length} 项更改等待同步到服务器。</span>}
      {conflicts.length>0&&<span>{conflicts.length} 项更改与服务器版本冲突，需要你选择保留哪一版。</span>}
      {conflicts.length>0&&<div className="sync-conflicts">{conflicts.slice(0,3).map(row=><div key={row.id}><span>{row.lastError||row.conflict?.message||'版本冲突'}</span><button onClick={()=>void retryOutbox(row.id)}>重试</button><button onClick={()=>void discardOutbox(row.id)}>放弃本地更改</button></div>)}</div>}
    </div>
    {online&&(pending.length>0||conflicts.length>0)&&<button className="sync-refresh" disabled={busy} onClick={async()=>{setBusy(true);await flushOutbox();setBusy(false);}} aria-label="立即同步"><RefreshCw size={14}/>{busy?'同步中…':'立即同步'}</button>}
  </div>;
}
