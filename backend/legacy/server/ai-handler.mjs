import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';

export function getAIConfiguration(env=process.env) {
  let endpoint='';
  try {
    const url=new URL(env.AI_ENDPOINT||'');
    if(['http:','https:'].includes(url.protocol)) {
      // Status is public application metadata, never a credential channel.
      url.username='';url.password='';url.search='';url.hash='';
      endpoint=url.href;
    }
  } catch { /* Incomplete configuration is reported explicitly. */ }
  return {
    configured:!!endpoint&&!!env.AI_MODEL?.trim(),
    gatewayName:env.AI_GATEWAY_NAME?.trim()||'服务端 AI 网关',
    endpoint,
    model:env.AI_MODEL?.trim()||'',
    authConfigured:!!env.AI_API_KEY?.trim(),
  };
}

export function createAIHandler(env=process.env) {
  let active=0;
  const json=(res,status,value)=>{res.writeHead(status,{'Content-Type':'application/json; charset=utf-8','Cache-Control':'no-store'});res.end(JSON.stringify(value));};
  return async (req,res,next=()=>{})=>{
    const route=req.url?.split('?')[0];
    if(route==='/api/ai-status'&&req.method==='GET') {json(res,200,getAIConfiguration(env));return;}
    if(route!=='/api/ai'){next();return;}
    if(req.method!=='POST'){json(res,405,{error:'只支持 POST 请求。'});return;}
    const origin=req.headers.origin;
    if(origin){try{if(new URL(origin).host!==req.headers.host){json(res,403,{error:'不允许跨站调用本地 AI 转发。'});return;}}catch{json(res,403,{error:'无效来源。'});return;}}
    if(!env.AI_ENDPOINT||!env.AI_MODEL){json(res,503,{error:'尚未配置 AI。请在设置中填写兼容接口，或在服务端配置 AI_ENDPOINT 与 AI_MODEL。'});return;}
    if(active>=3){json(res,429,{error:'AI 正在处理其他请求，请稍后重试。'});return;}
    active++;
    const abort=new AbortController();
    const timer=setTimeout(()=>abort.abort(),120000);
    res.on('close',()=>{if(!res.writableEnded)abort.abort();});
    try {
      let body='';for await(const chunk of req){body+=chunk;if(Buffer.byteLength(body)>180000){json(res,413,{error:'本次内容过长，请按段点评。'});return;}}
      let payload;try{payload=JSON.parse(body);}catch{json(res,400,{error:'请求格式不正确。'});return;}
      if(!Array.isArray(payload.messages)||!payload.messages.length||payload.messages.length>16||payload.messages.some(m=>!['system','user','assistant'].includes(m.role)||typeof m.content!=='string')){json(res,400,{error:'对话内容格式不正确。'});return;}
      const response=await fetch(env.AI_ENDPOINT,{method:'POST',headers:{'Content-Type':'application/json',...(env.AI_API_KEY?{Authorization:`Bearer ${env.AI_API_KEY}`}:{})},body:JSON.stringify({model:env.AI_MODEL,messages:payload.messages,stream:true,temperature:0.2,max_tokens:2600}),signal:abort.signal});
      if(!response.ok){json(res,response.status,{error:`模型服务返回 ${response.status}，请检查服务端模型、额度与接口配置。`});return;}
      res.writeHead(200,{'Content-Type':response.headers.get('content-type')||'text/event-stream','Cache-Control':'no-cache','X-Accel-Buffering':'no'});
      if(!response.body)throw new Error('empty body');
      await pipeline(Readable.fromWeb(response.body),res);
    }catch(error){if(!res.headersSent)json(res,502,{error:abort.signal.aborted?'AI 请求超时或已取消。':'无法连接模型服务，请检查接口配置与网络。'});else res.destroy();}
    finally{clearTimeout(timer);active--;}
  };
}
