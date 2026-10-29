import 'fake-indexeddb/auto';
import {beforeEach,expect,it,vi} from 'vitest';
import {api,ApiError} from './api';
import {syncDb} from './sync';
import {enqueue,flushOutbox} from './outbox';

vi.mock('./api',async original=>({...await original<typeof import('./api')>(),api:vi.fn()}));
beforeEach(async()=>{
  vi.stubGlobal('window',{dispatchEvent:()=>true});
  await syncDb.outbox.clear();vi.mocked(api).mockReset();
});

it.each(['success','network-error'] as const)('keeps newer changes when an older request returns %s',async outcome=>{
  let finish!:(value:unknown)=>void,fail!:(error:Error)=>void;
  vi.mocked(api).mockImplementationOnce(()=>new Promise((resolve,reject)=>{finish=resolve;fail=reject;})).mockResolvedValue({});
  await enqueue({dedupeKey:'reading:m',kind:'reading',path:'/api/learning/reading',body:{progress:.25,done:true}});
  await vi.waitFor(()=>expect(api).toHaveBeenCalledTimes(1));
  await enqueue({dedupeKey:'reading:m',kind:'reading',path:'/api/learning/reading',body:{progress:.75}});
  if(outcome==='success')finish({});else fail(new ApiError(0,{code:'network_error',message:'offline'}));
  await flushOutbox();
  expect(api).toHaveBeenCalledTimes(2);
  expect(vi.mocked(api).mock.calls[1][1]?.json).toEqual({progress:.75,done:true});
  expect((await syncDb.outbox.toArray())[0]).toMatchObject({state:'synced',body:{progress:.75,done:true}});
});
