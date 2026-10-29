import fs from 'node:fs/promises';
import path from 'node:path';

// Deterministic, inspectable retrieval. No model calls, no fabricated associations.
const segmenter=new Intl.Segmenter('zh',{granularity:'word'});
const stop=new Set('系统 软件 架构 设计 技术 方法 数据 服务 功能 过程 结构 模型 应用 概念 以下 正确 错误 关于 包括 可以 进行 实现 通过 使用 一个 一种 主要 相关 方式 提供 分析 下列 其中 需要 要求 哪个 哪些 不同 采用 支持 具有 特点 属于 之间 能够 下述 通常 工作 表示 问题 对于 根据 采用 信息 计算机 开发 内容 说明 定义 用户 处理 例如 以及 这种 一般 分为 方面 作用 组成 基本 对应 描述 可能 必须 满足 存在 需要 时期 基础 同时 经典 机制 类型 关系 是否 反映 抽取 自动 领域 目前 现在 这种 这些 以上 中的 以及 还要 只有 不能 成为 已经 解决 传统 研究 核心 全部 必须 提高 产生 实际 的是 的有 的不 不是 所有 执行 指定 主要 标准 模式 the following which and that this what with from for are can not use software system design development'.split(' '));
const concepts=[
  ['ATAM','架构权衡分析','效用树'],['SAAM','软件架构分析方法'],['CBAM','成本效益分析法'],
  ['访问控制矩阵','访问矩阵','ACM'],['范式','数据库范式','1NF','2NF','3NF','BCNF'],
  ['函数依赖','functional dependency'],['死锁','deadlock','银行家算法'],['信号量','PV操作','P操作','V操作','semaphore'],
  ['CAP','CAP定理'],['ACID','事务特性'],['哈夫曼','Huffman'],['流水线','pipeline'],
  ['微服务','microservice'],['熔断','断路器','circuit breaker'],['CQRS','命令查询职责分离','命令查询分离'],
  ['领域驱动设计','DDD','限界上下文'],['消息队列','message queue'],['负载均衡','load balancing'],
  ['软件可靠性','可靠性模型'],['数字签名','digital signature'],['公钥','非对称加密','RSA'],
  ['设计模式','design pattern'],['单例','singleton'],['观察者','observer'],['适配器','adapter'],
  ['MVC','模型视图控制器'],['SOA','面向服务架构'],['DSSA','特定领域软件体系结构'],['ABSD','基于架构的软件开发'],
  ['白盒测试','white box'],['黑盒测试','black box'],['子网掩码','subnet mask'],['关键路径','CPM'],
  ['Cache','高速缓冲存储器','高速缓存'],['子网','子网划分','子网掩码','广播地址'],['量子计算','量子比特','qubit'],['后量子密码','Post-Quantum','CRYSTALS-Kyber','ML-KEM'],['Transformer','自注意力','Self-Attention'],['AI对齐','AI Alignment'],['开源许可证','GPL','Apache许可证'],['逆向工程','reverse engineering'],['解释器','interpreter'],['软件维护','正确性维护','改正性维护','适应性维护','完善性维护'],['边缘计算','edge computing'],['数字孪生','digital twin'],['云原生','cloud native'],['区块链','blockchain']
];
const has=(text,term)=>/^[a-z0-9 ]+$/i.test(term)?new RegExp(`\\b${term}\\b`,'i').test(text):text.includes(term.toLowerCase());
function terms(text){
  const lower=text.toLowerCase();
  const tokens=[...segmenter.segment(lower)].filter(s=>s.isWordLike&&s.segment.length>=2&&!/^\d+$/.test(s.segment)&&!stop.has(s.segment)).map(s=>s.segment);
  for(const group of concepts)if(group.some(t=>has(lower,t)))tokens.push(`concept:${group[0]}`);
  return tokens;
}
export async function linkKnowledge(choices,materials,contentRoot){
  const docs=[];
  for(const material of materials.filter(m=>['textbook','notes'].includes(m.kind))){
    // Prefaces and thesis-writing guidance do not have matching multiple-choice exercises.
    if(material.title==='前言'||material.title.includes('论文'))continue;
    const text=await fs.readFile(path.join(contentRoot,'materials',`${material.id}.md`),'utf8');
    for(let index=0;index<material.sections.length;index++){
      const section=material.sections[index];if(section.level===1)continue;
      const body=text.slice(section.start,material.sections[index+1]?.start);
      if(body.trim().length<55)continue;
      const ancestors=[];
      for(const previous of material.sections.slice(0,index)){while(ancestors.length&&ancestors.at(-1).level>=previous.level)ancestors.pop();ancestors.push(previous);}
      while(ancestors.length&&ancestors.at(-1).level>=section.level)ancestors.pop();
      const headingTerms=new Set(terms(section.title));
      const parentTerms=new Set(terms(ancestors.filter(s=>s.level>1).map(s=>s.title).join(' ')));
      const bodyTerms=terms(body),counts=new Map();for(const token of bodyTerms)counts.set(token,(counts.get(token)||0)+1);
      docs.push({material,section,ancestors,headingTerms,parentTerms,counts,length:bodyTerms.length});
    }
  }
  const frequency=new Map();for(const doc of docs)for(const token of doc.counts.keys())frequency.set(token,(frequency.get(token)||0)+1);
  const idf=token=>Math.log(1+(docs.length-(frequency.get(token)||0)+0.5)/((frequency.get(token)||0)+0.5));
  const average=docs.reduce((n,d)=>n+d.length,0)/docs.length;
  const links={};
  for(const q of choices){
    const weights=new Map();
    for(const [text,weight] of [[q.stem,1],[/自动抽取|待补|暂无|未知/.test(q.knowledge||'')?'':q.knowledge||'',2.5],[q.options?.[q.answer]||'',1.2]])for(const token of new Set(terms(text)))if(frequency.has(token)&&(token.startsWith('concept:')||frequency.get(token)<docs.length*.20))weights.set(token,Math.max(weights.get(token)||0,weight));
    const focus=terms(q.stem).filter(t=>t.startsWith('concept:')&&!['concept:微服务','concept:设计模式'].includes(t));
    const named=[...q.stem.matchAll(/\b(?:[A-Z][A-Z0-9-]{1,}|Cache|Transformer|Huffman)\b/g)].map(m=>m[0].toLowerCase()).filter(t=>!/^p[0-9]+$/.test(t));
    const matches=[];
    for(const doc of docs){
      if(focus.length&&!focus.some(t=>doc.counts.has(t)))continue;
      if(!focus.length&&named.length&&!named.some(t=>doc.counts.has(t)))continue;
      let score=0;const evidence=[];
      for(const [token,weight] of weights){
        const count=doc.counts.get(token)||0;if(!count)continue;
        const termScore=idf(token)*weight*(count*2.2/(count+1.2*(.25+.75*doc.length/average))+(doc.headingTerms.has(token)?2.5:doc.parentTerms.has(token)?.35:0));
        score+=termScore;evidence.push({token,value:termScore});
      }
      const concept=evidence.some(e=>e.token.startsWith('concept:'));
      const headline=evidence.some(e=>doc.headingTerms.has(e.token));
      if((evidence.length<2&&!concept)||score<16||(!headline&&!concept&&evidence.length<3))continue;
      if(doc.material.kind==='textbook')score*=1.15;
      matches.push({doc,score,evidence:evidence.sort((a,b)=>b.value-a.value)});
    }
    matches.sort((a,b)=>b.score-a.score||a.doc.material.id.localeCompare(b.doc.material.id));
    const best=matches[0];if(!best)continue;
    const textbook=matches.find(m=>m.doc.material.kind==='textbook'&&m.score>=best.score*.68);
    const selected=textbook?[textbook]:[];
    for(const match of matches){
      if(match.score<best.score*.80)break;
      if(selected.some(m=>m.doc.material.id===match.doc.material.id))continue;
      selected.push(match);if(selected.length===2)break;
    }
    links[q.id]=selected.map(({doc,score,evidence})=>({materialId:doc.material.id,sectionId:doc.section.id,sectionTitle:doc.section.title,sectionPath:doc.ancestors.map(s=>s.id),terms:evidence.map(e=>e.token.replace(/^concept:/,'')).filter((term,index,all)=>all.findIndex(t=>t.toLowerCase()===term.toLowerCase())===index).slice(0,4),score:Math.round(score*10)/10}));
  }
  return links;
}
