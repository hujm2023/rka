import 'fake-indexeddb/auto';
import {expect,it} from 'vitest';
import {bindAccount,accountRequest,suspendAccount} from './account';
import {db,selectLearningAccount} from './db';
import {syncDb,selectSyncAccount} from './sync';

it('switches account stores without exposing or discarding unsynced work',async()=>{
  selectLearningAccount('a');selectSyncAccount('a');
  await db.drafts.put({id:'same-essay',kind:'essay',values:{body:'private A'},updatedAt:new Date().toISOString()});
  await syncDb.outbox.put({id:'pending-a',kind:'writing',method:'PUT',path:'/api/writing/essay/same-essay',body:{values:{body:'private A'}},state:'local',createdAt:new Date().toISOString(),updatedAt:new Date().toISOString(),attempts:0});
  selectLearningAccount('b');selectSyncAccount('b');
  expect(await db.drafts.get('same-essay')).toBeUndefined();expect(await syncDb.outbox.count()).toBe(0);
  await db.delete();await syncDb.delete();
  selectLearningAccount('a');selectSyncAccount('a');
  expect((await db.drafts.get('same-essay'))?.values.body).toBe('private A');
  expect((await syncDb.outbox.get('pending-a'))?.state).toBe('local');
  await db.delete();await syncDb.delete();
});

it('binds requests to the page owner and aborts work on logout',()=>{
  expect(bindAccount('account-a')).toBe(true);
  const request=accountRequest();expect(request.headers['X-RKA-User-ID']).toBe('account-a');
  expect(bindAccount('account-b')).toBe(false);
  suspendAccount();expect(request.signal.aborted).toBe(true);
  expect(()=>accountRequest()).toThrow();
});
