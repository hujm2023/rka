import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAIHandler} from './server/ai-handler.mjs';
const dist=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../../frontend/dist');
if(!fs.existsSync(path.join(dist,'index.html')))throw new Error('先在项目根目录运行 make web。');
const mime={'.html':'text/html; charset=utf-8','.js':'text/javascript','.css':'text/css','.json':'application/json','.md':'text/plain; charset=utf-8','.svg':'image/svg+xml','.woff2':'font/woff2','.txt':'text/plain; charset=utf-8'};
const ai=createAIHandler();
http.createServer((req,res)=>ai(req,res,()=>{
  if(!['GET','HEAD'].includes(req.method)){res.writeHead(405);res.end();return;}
  let pathname;try{pathname=decodeURIComponent(new URL(req.url,'http://localhost').pathname);}catch{res.writeHead(400);res.end();return;}
  let file=path.resolve(dist,`.${pathname}`);
  if(!file.startsWith(`${dist}/`)&&file!==dist){res.writeHead(403);res.end();return;}
  if(!fs.existsSync(file)||fs.statSync(file).isDirectory()){
    if(path.extname(pathname)){res.writeHead(404);res.end('Not found');return;}
    file=path.join(dist,'index.html');
  }
  res.writeHead(200,{'Content-Type':mime[path.extname(file)]||'application/octet-stream','X-Content-Type-Options':'nosniff','Cache-Control':file.includes('/assets/')?'public,max-age=31536000,immutable':'no-cache'});
  if(req.method==='HEAD')res.end();else fs.createReadStream(file).pipe(res);
})).listen(Number(process.env.PORT||4173),process.env.HOST||'127.0.0.1',()=>console.log(`RKA: http://${process.env.HOST||'127.0.0.1'}:${process.env.PORT||4173}`));
