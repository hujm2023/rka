import {it} from 'node:test';
import assert from 'node:assert/strict';
import {getAIConfiguration} from './server/ai-handler.mjs';

it('reports actual gateway metadata without returning credentials',()=>{
  const result=getAIConfiguration({AI_GATEWAY_NAME:'Magpie',AI_ENDPOINT:'https://login:private-password@example.test/v1/chat/completions?api_key=private-query#private-fragment',AI_MODEL:'group/deepseek-failover',AI_API_KEY:'private-header-key'});
  assert.deepEqual(result,{configured:true,gatewayName:'Magpie',endpoint:'https://example.test/v1/chat/completions',model:'group/deepseek-failover',authConfigured:true});
  assert.ok(!JSON.stringify(result).includes('private-'));
});
it('does not claim malformed or incomplete configuration is ready',()=>{
  assert.equal(getAIConfiguration({AI_ENDPOINT:'not a URL',AI_MODEL:'model'}).configured,false);
  assert.equal(getAIConfiguration({AI_ENDPOINT:'ftp://example.test',AI_MODEL:'model'}).endpoint,'');
  assert.equal(getAIConfiguration({AI_ENDPOINT:'https://example.test',AI_MODEL:'  '}).configured,false);
});
