// Authentication state. The server owns the session (HttpOnly cookie); the
// browser only stores the projected user object in memory.
import {createContext,useCallback,useContext,useEffect,useRef,useState,type ReactNode} from 'react';
import {apiGet,apiPost,errorText} from './api';
import {accountOwner,bindAccount,suspendAccount} from './account';
import {selectLearningAccount} from './db';
import {selectSyncAccount} from './sync';

export type AuthUser = {
  id:string; email:string; displayName:string; role:'admin'|'learner'|string; status:string;
  mustChangePassword?:boolean; createdAt?:string; lastLoginAt?:string|null;
};
export type AuthSession = {id:string;expiresAt:string};
export type AuthStatus = 'loading'|'anonymous'|'authenticated';

type MeResponse = {user:AuthUser|null;capabilities?:{admin?:boolean};session?:AuthSession};

type AuthContextValue = {
  status:AuthStatus; user:AuthUser|null; capabilities:{admin:boolean}; session:AuthSession|null;
  login:(email:string,password:string)=>Promise<AuthUser>;
  logout:()=>Promise<void>;
  bootstrap:(input:{token:string;email:string;password:string;displayName:string})=>Promise<AuthUser>;
  acceptInvite:(input:{token:string;password:string;displayName:string})=>Promise<AuthUser>;
  changePassword:(currentPassword:string,newPassword:string)=>Promise<void>;
  refresh:()=>Promise<void>;
};

const signedOutKey='rka-signed-out';
const isSignedOut=()=>{try{return localStorage.getItem(signedOutKey)==='1';}catch{return false;}};
const rememberSignOut=(value:boolean)=>{try{if(value)localStorage.setItem(signedOutKey,'1');else localStorage.removeItem(signedOutKey);}catch{/* storage may be unavailable */}};
const AuthContext=createContext<AuthContextValue|null>(null);
export function useAuth(){const value=useContext(AuthContext);if(!value)throw new Error('Missing auth context');return value;}

export function AuthProvider({children}:{children:ReactNode}){
  const generation=useRef(0);
  const [status,setStatus]=useState<AuthStatus>('loading');
  const [user,setUser]=useState<AuthUser|null>(null);
  const [capabilities,setCapabilities]=useState({admin:false});
  const [session,setSession]=useState<AuthSession|null>(null);

  const apply=(data:MeResponse)=>{
    if(data?.user){
      const previous=accountOwner();
      if(!bindAccount(data.user.id)){suspendAccount();window.location.reload();return;}
      if(!previous){selectLearningAccount(data.user.id);selectSyncAccount(data.user.id);}
      setUser(data.user);setCapabilities({admin:data.capabilities?.admin===true||data.user.role==='admin'});setSession(data.session||null);setStatus('authenticated');}
    else{suspendAccount();setUser(null);setCapabilities({admin:false});setSession(null);setStatus('anonymous');}
  };

  const refresh=useCallback(async()=>{
    const epoch=generation.current;
    if(isSignedOut()){suspendAccount();setStatus('anonymous');return;}
    try{const data=await apiGet<MeResponse>('/api/auth/me');if(epoch===generation.current)apply(data);}
    catch(error){
      if(epoch!==generation.current)return;
      // A network outage must not destroy the local offline cache view.
      if(error instanceof Error&&/网络连接不可用/.test(error.message)){setStatus('anonymous');return;}
      setStatus('anonymous');
    }
  },[]);

  useEffect(()=>{void refresh();},[refresh]);
  useEffect(()=>{
    const handler=()=>{generation.current++;suspendAccount();setUser(null);setCapabilities({admin:false});setSession(null);setStatus('anonymous');};
    window.addEventListener('rka-unauthorized',handler);
    const storage=(event:StorageEvent)=>{if(event.key===signedOutKey){if(event.newValue==='1')handler();else window.location.reload();}};
    window.addEventListener('storage',storage);
    return()=>{window.removeEventListener('rka-unauthorized',handler);window.removeEventListener('storage',storage);};
  },[]);

  const login=useCallback(async(email:string,password:string)=>{
    generation.current++;suspendAccount();
    const data=await apiPost<MeResponse>('/api/auth/login',{email,password});
    if(!data.user) throw new Error('登录响应缺少用户信息。');
    rememberSignOut(false);apply(data);
    await refresh();
    return data.user;
  },[refresh]);

  const logout=useCallback(async()=>{
    generation.current++;rememberSignOut(true);suspendAccount();setStatus('loading');
    try{await apiPost('/api/auth/logout');}catch{/* clearing local state is still correct */}
    setUser(null);setCapabilities({admin:false});setSession(null);setStatus('anonymous');
  },[]);

  const bootstrap=useCallback(async(input:{token:string;email:string;password:string;displayName:string})=>{
    const data=await apiPost<MeResponse>('/api/auth/bootstrap',input);
    if(!data.user) throw new Error('初始化响应缺少用户信息。');
    rememberSignOut(false);await refresh();
    return data.user;
  },[refresh]);

  const acceptInvite=useCallback(async(input:{token:string;password:string;displayName:string})=>{
    const data=await apiPost<MeResponse>('/api/auth/invites/accept',input);
    if(!data.user) throw new Error('邀请响应缺少用户信息。');
    rememberSignOut(false);await refresh();
    return data.user;
  },[refresh]);

  const changePassword=useCallback(async(currentPassword:string,newPassword:string)=>{
    await apiPost('/api/auth/password',{currentPassword,newPassword});
  },[]);

  return <AuthContext.Provider value={{status,user,capabilities,session,login,logout,bootstrap,acceptInvite,changePassword,refresh}}>{children}</AuthContext.Provider>;
}

export function useErrorMessage(){return errorText;}
