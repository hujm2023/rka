// Pending-operation queue. Operations carry the client-generated event ID used
// by the server for idempotency, so a retry never duplicates an answer or task.
import {api,ApiError} from './api';
import {accountSuspended} from './account';
import {newId} from './id';
import {notifySync,syncDb,type OutboxRecord,type SyncStatus} from './sync';

export type OutboxEvent =
  | {type:'synced';record:OutboxRecord;data:unknown}
  | {type:'conflict';record:OutboxRecord}
  | {type:'failed';record:OutboxRecord;message:string}
  | {type:'changed'};

const listeners=new Set<(event:OutboxEvent)=>void>();
export function onOutbox(listener:(event:OutboxEvent)=>void){
  listeners.add(listener);
  return ()=>{listeners.delete(listener);};
}
function emit(event:OutboxEvent){
  for(const listener of listeners) listener(event);
  notifySync();
}

type EnqueueInput={id?:string;dedupeKey?:string;kind:string;method?:OutboxRecord['method'];path:string;body:unknown};

export async function enqueue(input:EnqueueInput):Promise<OutboxRecord>{
  const record=await syncDb.transaction('rw',syncDb.outbox,async()=>{
    const now=new Date().toISOString();
    const existing=input.dedupeKey?await syncDb.outbox.where('dedupeKey').equals(input.dedupeKey)
      .filter(row=>row.state==='local'||row.state==='pending').first():undefined;
    // Position updates omit done; preserve an explicit pending completion change.
    const body=existing&&input.kind==='reading'
      ? {...existing.body as object,...input.body as object}:input.body;
    const record:OutboxRecord=existing
      ? {...existing,body,revision:(existing.revision||0)+1,state:'local',updatedAt:now,retryAfter:undefined}
      : {id:input.id||newId(),dedupeKey:input.dedupeKey,kind:input.kind,method:input.method||'POST',
         path:input.path,body,revision:1,state:'local',createdAt:now,updatedAt:now,attempts:0};
    await syncDb.outbox.put(record);
    return record;
  });
  emit({type:'changed'});
  void flushOutbox();
  return record;
}

let flushing:Promise<void>|null=null;

// A completion only acknowledges the exact version sent over the network.
async function acknowledge(pending:OutboxRecord,next:OutboxRecord){
  return syncDb.transaction('rw',syncDb.outbox,async()=>{
    const current=await syncDb.outbox.get(pending.id);
    if(!current||(current.revision||0)!==(pending.revision||0))return false;
    await syncDb.outbox.put(next);
    return true;
  });
}

export function flushOutbox():Promise<void>{
  if(flushing)return flushing;
  flushing=(async()=>{
    while(!accountSuspended()){
      const pending=await syncDb.transaction('rw',syncDb.outbox,async()=>{
        const row=await syncDb.outbox.orderBy('createdAt').filter(row=>
          (row.state==='local'||row.state==='pending')&&(!row.retryAfter||Date.now()>=row.retryAfter)).first();
        if(!row)return;
        const next:OutboxRecord={...row,state:'pending',updatedAt:new Date().toISOString()};
        await syncDb.outbox.put(next);
        return next;
      });
      if(!pending)break;
      emit({type:'changed'});
      try{
        const data=await api(pending.path,{method:pending.method,json:pending.body});
        const synced:OutboxRecord={...pending,state:'synced',updatedAt:new Date().toISOString(),lastError:undefined,conflict:undefined};
        if(await acknowledge(pending,synced))emit({type:'synced',record:synced,data});
      }catch(error){
        if(accountSuspended()||(error as Error)?.name==='AbortError'||(error instanceof ApiError&&error.status===401))break;
        if(error instanceof ApiError&&error.status>=400&&error.status<500&&error.status!==429){
          const details=(error.details||{}) as Record<string,unknown>;
          const conflict:OutboxRecord={...pending,state:'conflict',updatedAt:new Date().toISOString(),lastError:error.message,
            conflict:{message:error.message,details:error.details,serverValues:details.serverValues,clientValues:details.clientValues,serverVersion:typeof details.serverVersion==='number'?details.serverVersion:undefined}};
          if(await acknowledge(pending,conflict))emit({type:'conflict',record:conflict});
        }else{
          const attempts=pending.attempts+1;
          const retry:OutboxRecord={...pending,state:'pending',attempts,updatedAt:new Date().toISOString(),lastError:errorMessage(error),
            retryAfter:Date.now()+Math.min(30000,1000*2**Math.min(attempts,5))};
          if(await acknowledge(pending,retry))emit({type:'failed',record:retry,message:errorMessage(error)});
        }
      }
    }
    emit({type:'changed'});
  })().finally(()=>{flushing=null;});
  return flushing;
}

function errorMessage(error:unknown){return error instanceof Error?error.message:String(error);}

export async function retryOutbox(id:string){
  const row=await syncDb.outbox.get(id);
  if(!row) return;
  await syncDb.outbox.put({...row,revision:(row.revision||0)+1,state:'local',retryAfter:undefined,lastError:undefined,conflict:undefined});
  emit({type:'changed'});
  await flushOutbox();
}

export async function discardOutbox(id:string){
  await syncDb.outbox.delete(id);
  emit({type:'changed'});
}

export async function updateOutboxBody(id:string,body:unknown){
  const row=await syncDb.outbox.get(id);
  if(!row) return;
  await syncDb.outbox.put({...row,body,revision:(row.revision||0)+1,state:'local',conflict:undefined,retryAfter:undefined});
  emit({type:'changed'});
}

export async function pendingRows(){
  return syncDb.outbox.where('state').anyOf('local','pending').toArray();
}

let started=false;
export function startOutbox(){
  if(started) return ()=>{};
  started=true;
  void flushOutbox();
  const online=async()=>{
    try{await syncDb.outbox.where('state').equals('pending').modify(row=>{row.retryAfter=undefined;});}catch{/* best effort */}
    void flushOutbox();
  };
  window.addEventListener('online',online);
  const wake=()=>{void flushOutbox();};
  const visibility=()=>{if(document.visibilityState==='visible')void flushOutbox();};
  window.addEventListener('focus',wake);
  document.addEventListener('visibilitychange',visibility);
  const timer=window.setInterval(()=>{void flushOutbox();},4000);
  void syncDb.outbox.where('state').equals('synced').filter(row=>Date.parse(row.updatedAt)<Date.now()-2*86400000).delete().catch(()=>{});
  return ()=>{
    started=false;
    window.removeEventListener('online',online);
    window.removeEventListener('focus',wake);
    document.removeEventListener('visibilitychange',visibility);
    window.clearInterval(timer);
  };
}

export type {SyncStatus};
