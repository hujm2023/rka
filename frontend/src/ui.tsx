import {createElement,memo,useEffect,useId,useRef,useState,type ButtonHTMLAttributes,type ReactNode} from 'react';
import {Slot} from '@radix-ui/react-slot';
import * as Dialog from '@radix-ui/react-dialog';
import {X,LoaderCircle,ArrowUpRight} from 'lucide-react';
import ReactMarkdown, {type Components} from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import rehypeKatex from 'rehype-katex';
import {useApp} from './context';
import {renderThemedDiagram} from './diagram';
export function Button({variant='primary',className='',asChild=false,...props}:ButtonHTMLAttributes<HTMLButtonElement>&{variant?:'primary'|'secondary'|'ghost'|'danger';asChild?:boolean}) {const C=asChild?Slot:'button';return <C className={`button ${variant} ${className}`} {...props}/>;}
export function Badge({children,tone='neutral'}:{children:ReactNode;tone?:string}){return <span className={`badge ${tone}`}>{children}</span>;}
export function Empty({title,children,action}:{title:string;children?:ReactNode;action?:ReactNode}){return <div className="empty"><div className="empty-symbol">○</div><h3>{title}</h3><p>{children}</p>{action}</div>;}
export function Loading(){return <div className="loading"><LoaderCircle className="spin" size={23}/> 正在准备学习内容…</div>;}
export function Modal({open,onOpenChange,title,description,children}:{open:boolean;onOpenChange:(value:boolean)=>void;title:string;description:string;children:ReactNode}){return <Dialog.Root open={open} onOpenChange={onOpenChange}><Dialog.Portal><Dialog.Overlay className="modal-overlay"/><Dialog.Content className="modal"><Dialog.Title>{title}</Dialog.Title><Dialog.Description>{description}</Dialog.Description><Dialog.Close className="icon-button modal-close" aria-label="关闭"><X size={20}/></Dialog.Close>{children}</Dialog.Content></Dialog.Portal></Dialog.Root>;}
function Diagram({code}:{code:string}) {
  const {theme}=useApp();
  const id=useId().replace(/[^a-z0-9]/gi,''),host=useRef<HTMLDivElement>(null);
  const [visible,setVisible]=useState(false),[rendered,setRendered]=useState({svg:'',theme:''}),[error,setError]=useState(false);
  useEffect(()=>{const observer=new IntersectionObserver(entries=>{if(entries.some(e=>e.isIntersecting)){setVisible(true);observer.disconnect();}},{rootMargin:'400px'});if(host.current)observer.observe(host.current);return()=>observer.disconnect();},[]);
  useEffect(()=>{if(!visible)return;let alive=true;setError(false);renderThemedDiagram(id,code,theme).then(svg=>{if(alive)setRendered({svg,theme});}).catch(()=>{if(alive)setError(true);});return()=>{alive=false;};},[code,id,visible,theme]);
  const svg=rendered.theme===theme?rendered.svg:'';
  return <div ref={host} className="diagram-host" data-diagram-theme={svg?theme:'loading'}>{error?<details><summary>图示暂无法渲染，查看原始描述</summary><pre>{code}</pre></details>:<div className="diagram">{svg?<div dangerouslySetInnerHTML={{__html:svg}}/>:<span className="diagram-loading">{visible?'正在绘制图示…':'图示将在阅读到此处时加载…'}</span>}</div>}</div>;
}
export const Markdown=memo(function Markdown({children,anchors=false,sourceUrl}:{children:string;anchors?:boolean;sourceUrl?:string}) {
  const components:Components={
    a:({href,children})=><a href={sourceUrl&&href?new URL(href,sourceUrl).href:href} target="_blank" rel="noreferrer">{children}</a>,
    code:({className,children,...props})=>className==='language-mermaid'?<Diagram code={String(children)}/>:<code className={className} {...props}>{children}</code>
  };
  if(anchors)for(const tag of ['h1','h2','h3','h4'] as const)components[tag]=({node,children})=>createElement(tag,{id:`section-${node?.position?.start.offset??0}`,'data-section-heading':true},children);
  return <div className="markdown"><ReactMarkdown remarkPlugins={[remarkGfm,remarkMath]} rehypePlugins={[rehypeKatex]} components={components}>{children}</ReactMarkdown></div>;
});
export function SourceLink({href,children='查看原始来源'}:{href:string;children?:ReactNode}){return <a className="source-link" href={href} target="_blank" rel="noreferrer">{children}<ArrowUpRight size={13}/></a>;}
export function PageTitle({eyebrow,title,description,action}:{eyebrow:string;title:string;description:string;action?:ReactNode}){return <div className="page-heading"><div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>{action}</div>;}
