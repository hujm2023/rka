let renderQueue:Promise<unknown>=Promise.resolve();
let serial=0;

export function renderThemedDiagram(id:string,code:string,theme:string){
  const style=getComputedStyle(document.documentElement);
  const color=(token:string)=>style.getPropertyValue(token).trim();
  const palette={panel:color('--panel'),soft:color('--soft'),surface:color('--sage'),ink:color('--ink'),muted:color('--muted'),accent:color('--accent'),line:color('--line'),amber:color('--amber'),amberBg:color('--amber-bg')};
  const renderId=`diagram${id}-${theme}-${++serial}`;
  // Mermaid configuration is global. Serialize initialize + render so a theme
  // switch cannot recolor another diagram that is still being rendered.
  const job=renderQueue.catch(()=>{}).then(async()=>{
    const {default:mermaid}=await import('mermaid');
    mermaid.initialize({
      startOnLoad:false,securityLevel:'strict',theme:'base',fontFamily:'sans-serif',
      secure:['securityLevel','startOnLoad','maxTextSize','theme','themeVariables'],
      themeVariables:{
        darkMode:theme==='dark',background:palette.panel,
        primaryColor:palette.surface,primaryTextColor:palette.ink,primaryBorderColor:palette.accent,
        secondaryColor:palette.soft,secondaryTextColor:palette.ink,secondaryBorderColor:palette.muted,
        tertiaryColor:palette.panel,tertiaryTextColor:palette.ink,tertiaryBorderColor:palette.line,
        textColor:palette.ink,lineColor:palette.muted,nodeTextColor:palette.ink,
        mainBkg:palette.surface,nodeBorder:palette.accent,
        clusterBkg:palette.soft,clusterBorder:palette.muted,edgeLabelBackground:palette.panel,
        titleColor:palette.ink,fontSize:'14px',
        actorBkg:palette.surface,actorBorder:palette.accent,actorTextColor:palette.ink,
        signalColor:palette.ink,signalTextColor:palette.ink,labelBoxBkgColor:palette.soft,
        labelBoxBorderColor:palette.muted,labelTextColor:palette.ink,loopTextColor:palette.ink,
        noteBkgColor:palette.amberBg,noteBorderColor:palette.amber,noteTextColor:palette.ink,
      },
      // The source's explicitly amber port class keeps its semantic color.
      // Only its display palette changes; the original Markdown stays intact.
      themeCSS:theme==='dark'?`.port > rect,.port > polygon,.port > path{fill:${palette.amberBg}!important;stroke:${palette.amber}!important}.port .nodeLabel,.port .nodeLabel p{color:${palette.ink}!important}`:'',
    });
    return (await mermaid.render(renderId,code)).svg;
  });
  renderQueue=job;
  return job;
}
