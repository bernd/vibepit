import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createConversation, beginTurn, applyFrame, endProgress, matchingCommands, insertCommand } from '../src/claude-stream.ts';
const feed = (s, frame, time = 2000) => applyFrame(s, frame, time);
const event = (s, event) => feed(s, {type:'stream_event', event});
test('initialization, optional metadata and freshest usage windows', () => {
 const s=createConversation();
 feed(s,{type:'control_response',response:{subtype:'success',request_id:'initialize',response:{account:{subscriptionType:'Claude Max'},commands:[{name:'review',description:'Review code'}]}}});
 feed(s,{type:'system',subtype:'init',apiKeySource:'none',slash_commands:['review','compact'],terminal_slash_commands:['theme']});
 feed(s,{type:'rate_limit_event',rate_limit_info:{isUsingOverage:false,unifiedWindows:{five_hour:{utilization:.71,resetsAt:123,status:'allowed'}}}});
 assert.equal(s.account.subscriptionType,'Claude Max'); assert.equal(s.rate.unifiedWindows.five_hour.utilization,.71);
 assert.deepEqual(s.commands.map(c=>c.name),['review','compact']);
 feed(s,{type:'system',subtype:'future'}); feed(s,{type:'rate_limit_event'});
 assert.equal(s.rate.unifiedWindows.five_hour.utilization,.71);
});
test('text deduplication, tool lifecycle and per-turn usage', () => {
 const s=createConversation(); beginTurn(s,'Inspect',1000);
 event(s,{type:'message_start',message:{id:'m1',model:'model-a',usage:{input_tokens:10,cache_read_input_tokens:90}}});
 event(s,{type:'content_block_start',index:0,content_block:{type:'thinking'}}); assert.equal(s.progress.label,'Thinking');
 event(s,{type:'content_block_delta',index:1,delta:{type:'text_delta',text:'Hello'}});
 feed(s,{type:'assistant',message:{id:'m1',content:[{type:'text',text:'Hello'}]}});
 assert.equal(s.messages.filter(m=>m.role==='assistant').length,1);
 feed(s,{type:'assistant',message:{id:'m1',content:[{type:'tool_use',id:'t1',name:'Bash',input:{description:'Run tests',command:'go test'}}]}});
 feed(s,{type:'assistant',message:{id:'m1',content:[{type:'tool_use',id:'t1',name:'Bash',input:{description:'Run tests'}}]}});
 feed(s,{type:'user',message:{content:[{type:'tool_result',tool_use_id:'t1',content:'go: command not found',is_error:false}]},tool_use_result:{stdout:'go: command not found',stderr:''}});
 assert.equal(s.messages.find(m=>m.tool)?.tool.state,'Completed'); assert.equal(s.messages.find(m=>m.tool)?.tool.title,'Run tests');
 event(s,{type:'message_start',message:{id:'m2',model:'model-a',usage:{input_tokens:2,cache_read_input_tokens:198}}});
 event(s,{type:'message_delta',usage:{output_tokens:20}});
 feed(s,{type:'result',duration_ms:4000,is_error:false,usage:{output_tokens:2000},modelUsage:{'model-a':{contextWindow:1000}},result:'Hello'});
 assert.equal(s.context.tokens,220); assert.equal(s.context.capacity,1000);
 const summary=s.messages.at(-1).summary; assert.equal(summary.toolCount,1); assert.equal(summary.durationMs,4000); assert.equal(summary.usage.output_tokens,2000);
 assert.equal(s.progress,null);
});
test('interrupt and a new turn cannot leave stale tools or context', () => {
 const s=createConversation();beginTurn(s,'one',1);
 feed(s,{type:'assistant',message:{content:[{type:'tool_use',id:'t',name:'Read',input:{file_path:'a.go'}}]}});
 endProgress(s,'Interrupted'); assert.equal(s.messages.find(m=>m.tool).tool.state,'Interrupted');
 beginTurn(s,'two',4);assert.equal(s.context,null);
 feed(s,{type:'result',is_error:true,errors:['failed']});assert.equal(s.messages.at(-1).summary.outcome,'Failed');
});
test('commands filter by description and insert without discarding arguments', () => {
 const commands=[{name:'review',description:'Check code'},{name:'compact',description:'Reduce context'}];
 assert.deepEqual(matchingCommands(commands,'/context').map(c=>c.name),['compact']);
 assert.equal(insertCommand('/rev src/app.ts','review'),'/review src/app.ts');
 assert.equal(insertCommand('/','compact'),'/compact ');
});
test('real abort reasons and withdrawn permissions stop waiting indicators', () => {
 for (const reason of ['aborted_streaming','aborted_tools']) {
  const s=createConversation();beginTurn(s,'work',0);
  feed(s,{type:'assistant',message:{content:[{type:'tool_use',id:'t1',name:'Bash'}]}});
  feed(s,{type:'result',is_error:false,terminal_reason:reason});
  assert.equal(s.messages.at(-1).summary.outcome,'Interrupted');
  assert.equal(s.messages.find(m=>m.tool).tool.state,'Interrupted');
 }
 const s=createConversation();beginTurn(s,'work',0);
 feed(s,{type:'control_request',request_id:'r1',request:{subtype:'can_use_tool',tool_use_id:'t1',tool_name:'Bash',input:{}}});
 feed(s,{type:'control_cancel_request',request_id:'r1'});
 assert.equal(s.messages.find(m=>m.tool).tool.state,'No result');
 assert.notEqual(s.progress?.label,'Waiting for your tool decision');
});
test('local slash command output appears without a model message', () => {
 const s=createConversation();beginTurn(s,'/context',1);
 feed(s,{type:'system',subtype:'local_command_output',content:'Context usage: 10%',uuid:'local-1'});
 feed(s,{type:'result',subtype:'success',is_error:false});
 assert.equal(s.messages.find(m=>m.role==='assistant')?.text,'Context usage: 10%');
 assert.equal(s.messages.at(-1).summary.toolCount,0);
});
test('missing request usage does not invent an empty context window', () => {
 const s=createConversation();beginTurn(s,'work',1);
 event(s,{type:'message_start',message:{id:'no-usage',model:'m'}});
 feed(s,{type:'result',modelUsage:{m:{contextWindow:200000}}});
 assert.equal(s.context,null);
});
