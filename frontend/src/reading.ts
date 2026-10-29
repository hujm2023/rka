import {apiGet} from './api';
import {db} from './db';
import {syncDb} from './sync';
import type {LearningSummary,Reading} from './types';

export async function acceptReadingSummary(summary:LearningSummary){
  const pending=await syncDb.outbox.where('state').anyOf('local','pending','conflict').toArray();
  const protectedIds=new Set(pending.filter(row=>row.kind==='reading').map(row=>row.dedupeKey));
  await db.transaction('rw',db.reading,async()=>{
    for(const row of summary.readingProgress||[]){
      if(protectedIds.has(`reading:${row.materialId}`))continue;
      const value:Reading={id:row.materialId,progress:row.progress,done:row.done,updatedAt:row.updatedAt,
        sectionId:row.sectionAnchor,sectionOffset:row.sectionOffset};
      await db.reading.put(value);
    }
  });
}

export async function restoreReading(materialId:string){
  try{await acceptReadingSummary(await apiGet<LearningSummary>('/api/learning/summary'));}
  catch{/* Offline reading resumes from this account's cache. */}
  return db.reading.get(materialId);
}
