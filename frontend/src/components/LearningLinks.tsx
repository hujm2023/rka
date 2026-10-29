import {Link} from '@tanstack/react-router';
import {ArrowRight,BookOpen,Layers} from 'lucide-react';
import {useApp} from '../context';
import {belongsToMaterial,latestAttempts} from '../study';
import type {Material,Question} from '../types';
import {Badge,Button} from '../ui';

export function ChapterPracticeButton({material}:{material:Material}){
  const {catalog}=useApp();
  const count=catalog.choices.filter(q=>!q.figureMissing&&belongsToMaterial(catalog.knowledgeLinks||{},q.id,material.id)).length;
  return count?<Button asChild><Link to="/practice" search={{mode:'chapter',material:material.id,start:'1'}}><Layers size={15}/>练本章（{count}题）</Link></Button>:null;
}

export function ChapterExercises({material,sectionId}:{material:Material;sectionId?:string}){
  const {catalog,attempts}=useApp();
  const links=catalog.knowledgeLinks||{},latest=latestAttempts(attempts);
  const questions=catalog.choices.filter(q=>!q.figureMissing&&belongsToMaterial(links,q.id,material.id));
  const sectionQuestions=sectionId?questions.filter(q=>belongsToMaterial(links,q.id,material.id,sectionId)):[];
  const answered=questions.filter(q=>latest.has(q.id)).length;
  const wrong=questions.filter(q=>latest.get(q.id)?.correct===false).length;
  return <section className="chapter-exercises" aria-label="章节关联练习"><div className="chapter-exercises-heading"><span className="exercise-icon"><Layers size={23}/></span><div><h3>学完这一章，练一组对应的题。</h3><p>{questions.length?`${questions.length} 道关联选择题 · 已练 ${answered} 道 · 待巩固错题 ${wrong} 道`:'暂未匹配到可练的选择题。可以继续阅读其他章节。'}</p></div></div>
    {questions.length>0&&<div className="chapter-exercise-actions"><Button asChild><Link to="/practice" search={{mode:'chapter',material:material.id,start:'1'}}>开始本章练习 <ArrowRight size={16}/></Link></Button>{sectionQuestions.length>0&&<Button asChild variant="secondary"><Link to="/practice" search={{mode:'chapter',material:material.id,section:sectionId,start:'1'}}>练当前小节（{sectionQuestions.length}题）</Link></Button>}{wrong>0&&<Button asChild variant="secondary"><Link to="/practice" search={{mode:'wrong',material:material.id,start:'1'}}>巩固本章错题（{wrong}题）</Link></Button>}</div>}
    <small>根据题干、已有知识点与原文自动关联；疑似缺图题不计入。关联不代表官方考点归类。</small>
  </section>;
}

export function QuestionKnowledgeLinks({question,wrong=false,compact=false}:{question:Question;wrong?:boolean;compact?:boolean}){
  const {catalog}=useApp();
  const links=(catalog.knowledgeLinks?.[question.id]||[]).flatMap(link=>{
    const material=catalog.materials.find(m=>m.id===link.materialId);
    return material?.sections.some(s=>s.id===link.sectionId)?[{...link,material}]:[];
  });
  return <section className={compact?'related-lessons linked-lessons':'question-knowledge'} aria-label="本题关联原文"><div className="knowledge-links-heading"><h4><BookOpen size={16}/>{wrong?'回到原文，补齐这道题':'本题对应的知识原文'}</h4>{!compact&&<Badge>按知识点匹配</Badge>}</div>
    {links.length?links.map(link=><Link className="knowledge-source-link" to="/knowledge" search={{id:link.materialId,section:link.sectionId,from:'practice'}} key={`${link.materialId}:${link.sectionId}`}><div><strong>{link.sectionTitle}</strong><span>{link.material.title}</span>{!compact&&<small>关联词：{link.terms.join('、')}</small>}</div><ArrowRight size={16}/></Link>):<p className="unmapped-note">暂未建立可靠的小节关联。<Link to="/knowledge">前往知识手册查阅</Link></p>}
    {!compact&&links.length>0&&<p className="return-practice-note">打开后会定位到具体原文，顶部可返回本题继续练习。</p>}
  </section>;
}
