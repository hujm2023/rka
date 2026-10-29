import {expect,it,vi} from 'vitest';
import {consumeAIResponse} from './ai';
function sse(chunks:string[]){return new Response(new ReadableStream({start(controller){for(const chunk of chunks)controller.enqueue(new TextEncoder().encode(chunk));controller.close();}}),{headers:{'Content-Type':'text/event-stream'}});}
it('handles chunk boundaries and CRLF in a streamed response',async()=>{const onText=vi.fn();const result=await consumeAIResponse(sse(['data: {"choices":[{"delta":{"content":"先理解"}}]}\r\n','\r\ndata: {"choices":[{"delta":{"content":"概念 [S1]"}}]}\r\n\r\ndata: [DONE]\r\n\r\n']),onText);expect(result).toBe('先理解概念 [S1]');expect(onText).toHaveBeenLastCalledWith(result);});
it('does not treat an interrupted stream as a complete saved answer',async()=>{await expect(consumeAIResponse(sse(['data: {"choices":[{"delta":{"content":"未完成"}}]}\n\n']),()=>{})).rejects.toThrow('中断');});
it('shows provider errors rather than fabricating an explanation',async()=>{await expect(consumeAIResponse(new Response(JSON.stringify({error:{message:'模型不存在'}}),{status:400}),()=>{})).rejects.toThrow('模型不存在');});
it('supports providers that return a complete JSON response',async()=>{expect(await consumeAIResponse(new Response(JSON.stringify({choices:[{message:{content:'知识解释'}}]}),{headers:{'content-type':'application/json'}}),()=>{})).toBe('知识解释');});
