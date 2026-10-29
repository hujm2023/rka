// IndexedDB cache and the persistent pending-operation queue. The server is
// authoritative; this database lets the interface work offline and retry with
// the same client event identifier.
import Dexie,{type EntityTable} from 'dexie';

export type SyncStatus = 'local'|'pending'|'synced'|'conflict';

export type ConflictInfo = {
  message:string;
  details?:unknown;
  serverValues?:unknown;
  clientValues?:unknown;
  serverVersion?:number;
};

export type OutboxRecord = {
  id:string;
  revision?:number;
  dedupeKey?:string;
  kind:string;
  method:'POST'|'PUT'|'PATCH'|'DELETE';
  path:string;
  body:unknown;
  state:SyncStatus;
  createdAt:string;
  updatedAt:string;
  attempts:number;
  lastError?:string;
  conflict?:ConflictInfo;
  retryAfter?:number;
};

export type ConflictRecord = {
  id:string;
  kind:string;
  title:string;
  clientValues?:unknown;
  serverValues?:unknown;
  serverVersion?:number;
  message:string;
  createdAt:string;
  resolvedAt?:string;
};

export type CacheRecord = {key:string;value:unknown;savedAt:string};

class SyncDB extends Dexie {
  cache!:EntityTable<CacheRecord,'key'>;
  outbox!:EntityTable<OutboxRecord,'id'>;
  conflicts!:EntityTable<ConflictRecord,'id'>;
}

export function openSyncDB(name:string){
  const instance=new Dexie(name) as SyncDB;
  instance.version(1).stores({cache:'key',outbox:'id,state,createdAt,dedupeKey',conflicts:'id,createdAt'});
  return instance;
}
export let syncDb=openSyncDB('rka-sync-v1');
export function selectSyncAccount(id:string){syncDb.close();syncDb=openSyncDB(`rka-sync-user-${id}`);}

export async function cacheGet<T>(key:string):Promise<{value:T;savedAt:string}|null>{
  const row=await syncDb.cache.get(key);
  return row?{value:row.value as T,savedAt:row.savedAt}:null;
}

export async function cachePut(key:string,value:unknown){
  await syncDb.cache.put({key,value,savedAt:new Date().toISOString()});
}

export async function cacheRemove(key:string){
  await syncDb.cache.delete(key);
}

export function overallStatus(rows:OutboxRecord[]):SyncStatus{
  if(rows.some(row=>row.state==='conflict')) return 'conflict';
  if(rows.some(row=>row.state==='local'||row.state==='pending')) return 'pending';
  return 'synced';
}

export function syncStatusText(status:SyncStatus):string{
  switch(status){
    case 'local': return '本地保存';
    case 'pending': return '待同步';
    case 'conflict': return '有冲突';
    default: return '已同步';
  }
}

export function notifySync(){
  window.dispatchEvent(new CustomEvent('rka-sync-changed'));
}
