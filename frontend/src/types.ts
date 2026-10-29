// Shared domain types. The browser treats the server as authoritative and keeps
// IndexedDB only as a cache and offline queue.

export type Option = 'A' | 'B' | 'C' | 'D';

// Question origin. "real" is a real or recalled exam paper, "mock" is a practice
// paper, "ai" is generated practice. The three are labelled separately so AI
// practice is never presented as a real exam question.
export type SourceType = 'real' | 'mock' | 'ai';
export function sourceTypeLabel(value:string):string {
  if(value==='ai') return 'AI 生成练习';
  if(value==='mock') return '模拟题';
  return '真题 / 回忆版';
}

export type Figure = {kind:string;layers?:string[];code?:string;caption?:string;alt?:string};

export type Question = {
  id:string; revision:string; stem:string; options:Record<Option,string>;
  // answer/analysis are never part of the catalog API. They are filled from
  // /solution or from a submitted exam result and may be absent.
  answer?:Option; analysis?:string;
  term:string; paper?:string; questionNo:number; module:string; knowledge:string;
  difficulty?:string; sourceType:SourceType; sourceFile?:string; sourceLabel?:string; sourceUrl:string;
  figureMissing:boolean; multiBlank:boolean; figure?:Figure|null; reviewStatus?:string;
};

export type Case = {
  id:string; revision:string; title:string; description?:string; module:string; term:string;
  sourceType:SourceType; sourceUrl:string;
  subQuestions:{prompt:string;referenceAnswer?:string;reference_answer?:string}[];
};

export type Essay = {id:string;revision:string;title:string;prompt:string;writingPoints:string;module:string;term:string;sourceType:SourceType;sourceUrl:string};

export type Lesson = {id:string;title:string;book:string;module:string;sourceFile:string;sourceUrl:string;license:string;chars:number;minutes:number;excerpt:string};

export type MaterialSection = {id:string;title:string;level:number;start:number;key?:string;path?:string[]};

export type Material = {
  id:string; title:string; book:string; kind:'textbook'|'outline'|'notes'|'guide'; module:string;
  sourceFile:string; sourceUrl:string; license:string; chars:number; minutes:number;
  sections:MaterialSection[]; sourceHash?:string; excerpt:string;
  version?:number; versionId?:string; currentVersionId?:string; indexStatus?:string; status?:string;
};

export type KnowledgeLink = {materialId:string;sectionId:string;sectionKey?:string;sectionTitle:string;sectionPath:string[];terms:string[];score:number;status?:string};
export type KnowledgeLinks = Record<string,KnowledgeLink[]>;

export type Catalog = {
  version:string; serverTime?:string; choices:Question[]; cases:Case[]; essays:Essay[];
  lessons:Lesson[]; materials:Material[]; knowledgeLinks?:KnowledgeLinks;
};

export type Attempt = {id:string; questionId:string; revision:string; answer:Option;correct:boolean;assisted:boolean;createdAt:string;durationMs:number};
export type Mark = {id:string;favorite:boolean;uncertain:boolean;note:string;version?:number};

export type Filters = {mode:'chapter'|'all'|'exam'|'wrong'|'favorites'|'review';module:string;term:string;source:string;query:string;count:number;includeMissing:boolean;materialId?:string;sectionId?:string};

export type PracticeMode = Filters['mode'];

export type AnswerState = {selected?:Option;submitted?:boolean;assisted?:boolean;attemptId?:string;correct?:boolean;revealed?:boolean;pending?:boolean;sync?:SyncStateLabel};

export type Session = {id:string;questionIds:string[];index:number;answers:Record<string,AnswerState>;filters:Filters;elapsed:number;updatedAt:string;bankVersion:string;finished?:boolean};

// The four states from the platform contract. They are shown in the UI.
export type SyncStateLabel = 'local'|'pending'|'synced'|'conflict';

export type Solution = {answer?:Option;analysis?:string;scoringRule?:Record<string,unknown>};

export type ServerSession = {
  id:string; mode:PracticeMode; scope:Record<string,string|undefined>;
  questionIds:string[]; revisions?:Record<string,string>;
  index:number; elapsedSeconds:number; status:string; bankVersion:string; version:number;
  createdAt:string; updatedAt:string; deadlineAt?:string|null; submittedAt?:string|null;
  score?:number|null; maxScore?:number|null;
  answers:Record<string,AnswerState & {solution?:Solution}>;
  finished?:boolean;
};

export type PracticeStats = {
  totalAttempts?:number; practicedQuestions?:number; totalQuestions?:number;
  correctRate?:number; independentCorrectRate?:number; assistedRate?:number;
  wrongCount?:number; dueCount?:number; todayAttempts?:number;
  domains?:{module?:string;total?:number;practiced?:number;correctRate?:number}[];
  recentActivity?:{date?:string;day?:string;count?:number;attempts?:number}[];
  recent?:{date?:string;day?:string;count?:number;attempts?:number}[];
  [key:string]:unknown;
};

export type LearningSummary = {
  readingProgress?:{materialId:string;progress:number;done:boolean;updatedAt:string;sectionAnchor?:string;sectionOffset?:number}[];
  reading?:number; assisted?:number; independent?:number; selfDeclared?:number;
  counts?:{reading?:number;assisted?:number;independent?:number;selfDeclared?:number;self_declared?:number};
  recent?:unknown[];
  [key:string]:unknown;
};

export type Draft = {id:string;kind:'case'|'essay';values:Record<string,string>;updatedAt:string};
export type Reading = {id:string;progress:number;done:boolean;updatedAt:string;sectionId?:string;sectionOffset?:number;sectionKey?:string};
export type ChatMessage = {id:string;role:'user'|'assistant';content:string;createdAt:string;sources?:Citation[]};
export type Chat = {id:string;messages:ChatMessage[];updatedAt:string};
export type Citation = {id:string;title:string;text:string;lessonId?:string;url?:string;materialId?:string;materialVersionId?:string;sectionId?:string};
export type AIContext = {id:string;title:string;kind:'question'|'lesson'|'case'|'essay';text:string;module?:string;hint?:boolean;onHint?:()=>void;questionId?:string;questionRevision?:string;sessionId?:string;materialId?:string;materialVersionId?:string;sectionId?:string;documentId?:string;documentVersion?:number};
export type AISettings = {endpoint:string;model:string};

// Server writing document.
export type WritingDocument = {id:string;kind:'case'|'essay';targetId:string;values:Record<string,string>;version:number;updatedAt?:string};

export const modules:Record<string,string> = {architecture:'软件架构',software_engineering:'软件工程',database:'数据库系统',computer_foundation:'计算机基础',network:'计算机网络',security:'信息安全',project_management:'项目管理',new_technology:'新技术',english:'专业英语',embedded:'嵌入式系统',legal_ip:'法律与知识产权',other:'综合知识'};
export const defaultFilters:Filters = {mode:'all',module:'all',term:'all',source:'all',query:'',count:20,includeMissing:false};
