import {useState} from 'react';
import {apiGet,errorText} from '../api';
import {Button,Modal} from '../ui';

type Snapshot={id:string;version:number;source:string;createdAt:string;values?:Record<string,string>};
export function WritingHistory({kind,id,restore}:{kind:string;id:string;restore:(values:Record<string,string>)=>Promise<void>}){
  const [open,setOpen]=useState(false),[items,setItems]=useState<Snapshot[]>([]),[selected,setSelected]=useState<Snapshot|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const base=`/api/writing/${kind}/${encodeURIComponent(id)}`;
  async function show(){setOpen(true);setError('');setSelected(null);try{const data=await apiGet<{items:Snapshot[]}>(`${base}/versions`);setItems(data.items);}catch(e){setError(errorText(e));}}
  async function preview(row:Snapshot){try{const data=await apiGet<{version:Snapshot}>(`${base}/versions/${row.version}`,{source:row.source,snapshotId:row.id});setSelected(data.version);}catch(e){setError(errorText(e));}}
  async function useSnapshot(){if(!selected?.values)return;setBusy(true);try{await restore(selected.values);setOpen(false);}catch(e){setError(errorText(e));}finally{setBusy(false);}}
  return <><Button variant="secondary" onClick={()=>void show()}>查看写作历史</Button><Modal open={open} onOpenChange={setOpen} title="写作历史与冲突草稿" description="每份冲突单独保留。选择历史内容后，可以保存为新版本，现有历史不会被覆盖。">{error&&<p role="alert">{error}</p>}<div className="button-row">{items.map(row=><Button key={row.id} variant="secondary" onClick={()=>void preview(row)}>v{row.version} · {row.source==='conflict'?'冲突草稿':'已保存'} · {new Date(row.createdAt).toLocaleString()}</Button>)}</div>{!items.length&&<p>暂无写作历史。</p>}{selected&&<div>{Object.entries(selected.values||{}).map(([key,value])=><section key={key}><h4>{key}</h4><pre style={{whiteSpace:'pre-wrap',overflowWrap:'anywhere'}}>{value}</pre></section>)}<Button disabled={busy} onClick={()=>void useSnapshot()}>{busy?'保存中…':'以此版本继续写'}</Button></div>}</Modal></>;
}
