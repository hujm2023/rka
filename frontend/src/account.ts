// A document is bound to one account for its entire lifetime. A different
// cookie identity requires a reload, so late callbacks cannot switch databases.
let owner:string|undefined;
let suspended=false;
let controller=new AbortController();
export const accountOwner=()=>owner;
export const accountSuspended=()=>suspended;
export function bindAccount(id:string){
  if(owner&&owner!==id)return false;
  owner=id;suspended=false;
  if(controller.signal.aborted)controller=new AbortController();
  return true;
}
export function suspendAccount(){suspended=true;controller.abort();}
export function accountRequest(signal?:AbortSignal):{headers:Record<string,string>;signal:AbortSignal}{
  if(suspended)throw new DOMException('账号已退出或正在切换','AbortError');
  return {headers:owner?{'X-RKA-User-ID':owner}:{},signal:signal?AbortSignal.any([signal,controller.signal]):controller.signal};
}
