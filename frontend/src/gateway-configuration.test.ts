import {expect,it,vi} from 'vitest';
import {askAI} from './ai';

it('server mode leaves model and auth to the server; custom mode sends its own settings',async()=>{
  let request:RequestInit|undefined;
  vi.stubGlobal('fetch',async(_input:unknown,init:RequestInit)=>{request=init;return new Response(JSON.stringify({choices:[{message:{content:'ok'}}]}),{headers:{'Content-Type':'application/json'}});});
  try{
    await askAI({endpoint:'/api/ai',model:'stale-browser-model'},'browser-key',[{role:'user',content:'test'}],new AbortController().signal,()=>{});
    expect(request?.headers).not.toHaveProperty('Authorization');expect(JSON.parse(request!.body as string)).not.toHaveProperty('model');
    await askAI({endpoint:'https://example.test/v1/chat/completions',model:'custom-model'},'browser-key',[{role:'user',content:'test'}],new AbortController().signal,()=>{});
    expect(request?.headers).toHaveProperty('Authorization','Bearer browser-key');expect(JSON.parse(request!.body as string).model).toBe('custom-model');
  }finally{vi.unstubAllGlobals();}
});
