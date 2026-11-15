export interface Usage { input_tokens?: number; output_tokens?: number; cache_read_input_tokens?: number; cache_creation_input_tokens?: number; output_tokens_details?: { thinking_tokens?: number } }
export interface ModelUsage { contextWindow?: number; inputTokens?: number; outputTokens?: number; cacheReadInputTokens?: number; cacheCreationInputTokens?: number; thinkingTokens?: number }
export interface Command { name: string; description?: string; argumentHint?: string }
export interface Account { subscriptionType?: string; apiProvider?: string }
export interface RateWindow { utilization?: number; resetsAt?: number; status?: string }
export interface RateInfo { status?: string; rateLimitType?: string; resetsAt?: number; isUsingOverage?: boolean; overageStatus?: string; overageDisabledReason?: string; unifiedWindows?: Record<string, RateWindow> }
export interface Block { type: string; text?: string; id?: string; name?: string; input?: unknown; tool_use_id?: string; content?: unknown; is_error?: boolean }
export interface StreamFrame {
  type: string; subtype?: string; content?: string; uuid?: string; request_id?: string; session_id?: string; result?: string;
  is_error?: boolean; errors?: string[]; parent_tool_use_id?: string | null;
  message?: { id?: string; model?: string; usage?: Usage; content?: Block[] };
  request?: { subtype: string; tool_name: string; input: unknown; tool_use_id?: string };
  response?: { subtype?: string; request_id?: string; response?: { account?: Account; commands?: Command[] } };
  event?: { type: string; index?: number; message?: { id?: string; model?: string; usage?: Usage }; content_block?: Block; usage?: Usage; delta?: { type?: string; text?: string; partial_json?: string } };
  status?: string | null; estimated_tokens?: number; rate_limit_info?: RateInfo;
  model?: string; apiKeySource?: string; slash_commands?: string[]; commands?: Command[];
  duration_ms?: number; duration_api_ms?: number; usage?: Usage; modelUsage?: Record<string, ModelUsage>; terminal_reason?: string; stop_reason?: string;
  tool_use_result?: unknown; thinking_duration_ms?: number;
}
export type ToolState = 'Preparing' | 'Running' | 'Waiting for approval' | 'Completed' | 'Reported error' | 'Interrupted' | 'No result';
export interface ToolActivity { name: string; title: string; input: unknown; state: ToolState; startedAt: number; endedAt?: number; stdout?: string; stderr?: string; output?: string; filePath?: string; partial?: string }
export interface TurnSummary { outcome: string; durationMs?: number; apiMs?: number; toolCount: number; usage?: Usage; models?: Record<string, ModelUsage>; reason?: string }
export interface Message { id: string; role: 'user' | 'assistant' | 'tool' | 'summary'; text: string; messageID?: string; streamed?: boolean; tool?: ToolActivity; summary?: TurnSummary }
export interface Conversation {
  messages: Message[]; commands: Command[]; account: Account; apiKeySource?: string; rate: RateInfo | null; rateAt?: number;
  progress: { label: string; since: number; thinkingTokens?: number } | null;
  context: { tokens: number; capacity?: number; model: string } | null;
  currentMessage: string; currentUsage: Usage; capacities: Record<string, number>;
  nextID: number; turnStart: number; startedAt: number; blockTools: Record<string, string>; permissions: Record<string, string>; seen: Record<string, boolean>;
}
export function createConversation(): Conversation {
  return { messages: [], commands: [], account: {}, rate: null, progress: null, context: null, currentMessage: '', currentUsage: {}, capacities: {}, nextID: 0, turnStart: 0, startedAt: 0, blockTools: {}, permissions: {}, seen: {} };
}
export function phase(s: Conversation, label: string, now: number) {
  if (s.progress?.label !== label) s.progress = { label, since: now };
}
export function beginTurn(s: Conversation, prompt: string, now: number) {
  s.turnStart = s.messages.length; s.startedAt = now; s.currentMessage = ''; s.currentUsage = {}; s.context = null; s.permissions = {};
  s.messages.push({id: `user-${++s.nextID}`, role:'user', text:prompt}); phase(s,'Requesting response',now);
}
export function endProgress(s: Conversation, state: ToolState = 'No result', now = Date.now()) {
  s.progress = null;
  for (const m of s.messages.slice(s.turnStart)) if (m.tool && ['Preparing','Running','Waiting for approval'].includes(m.tool.state)) { m.tool.state = state; m.tool.endedAt = now; }
}
const object = (v: unknown): Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {};
export const formatValue = (v: unknown): string => typeof v === 'string' ? v : v == null ? '' : JSON.stringify(v, null, 2);
function toolTitle(name: string, input: unknown) {
  const args = object(input);
  return typeof args.description === 'string' ? args.description : typeof args.file_path === 'string' ? `${name === 'Read' ? 'Read' : name} ${args.file_path}` : name;
}
function upsertTool(s: Conversation, block: Block, state: ToolState, now: number): Message {
  const id = block.id || `unknown-tool-${++s.nextID}`;
  let row = s.messages.find(m => m.id === id);
  if (!row) { row = { id, role:'tool', text:'', tool: {name:block.name || 'Tool', title:toolTitle(block.name || 'Tool',block.input), input:block.input, state, startedAt:now} }; s.messages.push(row); }
  else if (row.tool && ['Preparing','Running','Waiting for approval'].includes(row.tool.state)) {
    if (block.input !== undefined) row.tool.input = block.input;
    row.tool.title = toolTitle(row.tool.name,row.tool.input);
    if (row.tool.state !== 'Waiting for approval') row.tool.state = state;
  }
  return row;
}
function updateContext(s: Conversation, usage?: Usage) {
  if (!s.context || !usage) return;
  s.currentUsage = { ...s.currentUsage, ...usage };
  const u=s.currentUsage;
  s.context.tokens = (u.input_tokens ?? 0) + (u.cache_read_input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0) + (u.output_tokens ?? 0);
}
function commands(items: Command[]): Command[] { return [...new Map(items.filter(c=>typeof c?.name === 'string' && c.name.length>0).map(c=>[c.name,c])).values()]; }
export function applyFrame(s: Conversation, f: StreamFrame, now = Date.now()) {
  if (f.uuid) { if (s.seen[f.uuid]) return; s.seen[f.uuid] = true; }
  if (f.type === 'control_response' && f.response?.request_id === 'initialize' && f.response.subtype === 'success') {
    const init=f.response.response;
    s.account = init?.account ? {subscriptionType:init.account.subscriptionType,apiProvider:init.account.apiProvider} : s.account;
    if (Array.isArray(init?.commands)) s.commands=commands(init.commands);
  }
  if (f.type === 'system' && f.subtype === 'local_command_output' && typeof f.content === 'string') s.messages.push({id:`command-${++s.nextID}`,role:'assistant',text:f.content});
  if (f.type === 'rate_limit_event' && f.rate_limit_info) { s.rate=f.rate_limit_info; s.rateAt=now; }
  if (f.type === 'system' && f.subtype === 'init') {
    s.apiKeySource=f.apiKeySource;
    if (Array.isArray(f.slash_commands)) s.commands=commands(f.slash_commands.map(name=>s.commands.find(c=>c.name===name) ?? {name}));
  }
  if (f.type === 'system' && f.subtype === 'commands_changed' && Array.isArray(f.commands)) s.commands=commands(f.commands);
  // Child tools remain visible, but must not replace main-thread progress/context.
  const main = !f.parent_tool_use_id;
  if (main && f.type === 'system' && f.subtype === 'status' && f.status) phase(s, f.status === 'requesting' ? 'Requesting response' : f.status === 'compacting' ? 'Compacting context' : f.status.replaceAll('_',' '), now);
  if (main && f.type === 'system' && f.subtype === 'thinking_tokens') { phase(s,'Thinking',now); if(s.progress) s.progress.thinkingTokens=f.estimated_tokens; }
  if (main && f.type === 'system' && f.subtype === 'compact_boundary') { s.context=null; s.currentUsage={}; }
  if (f.type === 'stream_event' && f.event && main) {
    const e=f.event;
    if (e.type === 'message_start') {
      s.currentMessage=e.message?.id || `stream-${++s.nextID}`; s.currentUsage={};
      const model=e.message?.model || '';
      const usage=e.message?.usage;
      s.context=usage && [usage.input_tokens,usage.cache_read_input_tokens,usage.cache_creation_input_tokens].some(v=>typeof v==='number') ? {model,tokens:0,capacity:s.capacities[model]} : null; updateContext(s,usage);
    }
    const key=`${s.currentMessage || 'stream'}:${e.index ?? 0}`;
    if (e.type === 'content_block_start') {
      if (e.content_block?.type === 'thinking') phase(s,'Thinking',now);
      if (e.content_block?.type === 'tool_use') {
        const row=upsertTool(s,e.content_block,'Preparing',now);s.blockTools[key]=row.id;phase(s,`Preparing ${row.tool!.name}`,now);
      }
    }
    if (e.delta?.type === 'input_json_delta') {
      const row=s.messages.find(m=>m.id===s.blockTools[key]); if(row?.tool) row.tool.partial=(row.tool.partial || '')+(e.delta.partial_json || '');
    }
    if (e.type === 'content_block_stop' && s.blockTools[key]) {
      const row=s.messages.find(m=>m.id===s.blockTools[key]);
      if(row?.tool?.partial) { try { row.tool.input=JSON.parse(row.tool.partial); row.tool.title=toolTitle(row.tool.name,row.tool.input); } catch { /* Wait for the complete assistant frame. */ } }
    }
    if (e.delta?.type === 'text_delta') {
      phase(s,'Writing reply',now);
      let row=s.messages.find(m=>m.id===`text-${key}`);
      if(!row) { row={id:`text-${key}`,role:'assistant',text:'',messageID:s.currentMessage,streamed:true};s.messages.push(row); }
      row.text+=e.delta.text || '';
    }
    if(e.type === 'message_delta') updateContext(s,e.usage);
  }
  if (f.type === 'assistant') {
    const blocks=f.message?.content ?? [];
    const text=blocks.filter(b=>b.type==='text').map(b=>b.text ?? '').join('\n');
    const id=f.message?.id || s.currentMessage || `assistant-${++s.nextID}`;
    if (text && main && !s.messages.some(m=>m.messageID===id && m.streamed)) {
      const row=s.messages.find(m=>m.id===`assistant-${id}`);
      if(row) row.text=text; else s.messages.push({id:`assistant-${id}`,role:'assistant',text,messageID:id});
    }
    for(const block of blocks.filter(b=>b.type==='tool_use')) { const row=upsertTool(s,block,'Running',now); if(main) phase(s,row.tool!.title,now); }
  }
  if (f.type === 'control_request' && f.request?.subtype === 'can_use_tool') {
    const id=f.request.tool_use_id;
    if (id && f.request_id) s.permissions[f.request_id] = id;
    if(id) upsertTool(s,{type:'tool_use',id,name:f.request.tool_name,input:f.request.input},'Waiting for approval',now).tool!.state='Waiting for approval';
    phase(s,'Waiting for your tool decision',now);
  }
  if (f.type === 'control_cancel_request' && f.request_id) {
    const row=s.messages.find(m=>m.id===s.permissions[f.request_id!]);
    if(row?.tool?.state==='Waiting for approval') { row.tool.state='No result'; row.tool.endedAt=now; }
    delete s.permissions[f.request_id];
    if(!Object.keys(s.permissions).length) phase(s,'Waiting for Claude',now);
  }
  if (f.type === 'user') for(const block of f.message?.content ?? []) if(block.type==='tool_result') {
    let row=s.messages.find(m=>m.id===block.tool_use_id);
    if(!row) row=upsertTool(s,{type:'tool_use',id:block.tool_use_id,name:'Tool'},'Running',now);
    const t=row.tool!; const result=object(f.tool_use_result); const file=object(result.file);
    t.state=result.interrupted === true ? 'Interrupted' : block.is_error ? 'Reported error' : 'Completed'; t.endedAt=now;
    t.stdout=typeof result.stdout==='string'?result.stdout:undefined; t.stderr=typeof result.stderr==='string'?result.stderr:undefined;
    t.filePath=typeof file.filePath==='string'?file.filePath:undefined;
    t.output=typeof file.content==='string'?file.content:t.stdout===undefined && t.stderr===undefined?formatValue(block.content):undefined;
    if(main) phase(s,'Waiting for Claude',now);
  }
  if (f.type === 'result') {
    const interrupted = ['interrupted','aborted_streaming','aborted_tools'].includes(f.terminal_reason || '');
    endProgress(s, interrupted ? 'Interrupted' : 'No result',now);
    s.permissions={};
    if(f.result && !s.messages.slice(s.turnStart).some(m=>m.role==='assistant')) s.messages.push({id:`result-${++s.nextID}`,role:'assistant',text:f.result});
    for(const [model,u] of Object.entries(f.modelUsage ?? {})) if(u.contextWindow) s.capacities[model]=u.contextWindow;
    if(s.context) s.context.capacity=s.capacities[s.context.model];
    s.messages.push({id:`summary-${++s.nextID}`,role:'summary',text:'',summary:{outcome:interrupted?'Interrupted':f.is_error?'Failed':f.terminal_reason && f.terminal_reason !== 'completed'?'Stopped':'Completed',durationMs:f.duration_ms ?? (s.startedAt?now-s.startedAt:undefined),apiMs:f.duration_api_ms,toolCount:s.messages.slice(s.turnStart).filter(m=>m.tool).length,usage:f.usage,models:f.modelUsage,reason:f.terminal_reason ?? f.stop_reason}});
  }
}
export function matchingCommands(commands: Command[], prompt: string): Command[] {
  const query=(prompt.match(/^\/([^\s]*)/)?.[1] ?? '').toLowerCase();
  return commands.filter(c=>`${c.name} ${c.description ?? ''}`.toLowerCase().includes(query));
}
export function insertCommand(prompt: string, name: string): string {
  const args=prompt.match(/^\/\S*(\s[\s\S]*)$/)?.[1];
  return `/${name}${args || ' '}`;
}
