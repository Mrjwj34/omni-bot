export const JSONRPC_VERSION = '2.0' as const;

export const RPC_METHODS = {
	CHANNEL_REGISTER: 'channel.register',
	CHANNEL_INBOUND: 'channel.inbound',
	CHANNEL_OUTBOUND: 'channel.outbound',
	UI_SUBSCRIBE: 'ui.subscribe',
	UI_UNSUBSCRIBE: 'ui.unsubscribe',
	UI_EVENT: 'ui.event',
	CHAT_SEND: 'chat.send',
	RUN_TRIGGER: 'run.trigger'
} as const;

export const UI_TOPICS = {
	TRACE_STEP: 'trace.step',
	PAD_UPDATE: 'pad.update',
	TIMELINE_APPEND: 'timeline.append',
	CONFIG_UPDATED: 'config.updated',
	CHANNEL_STATUS: 'channel.status'
} as const;

export type UITopic = (typeof UI_TOPICS)[keyof typeof UI_TOPICS];

export interface RPCError {
	code: number;
	message: string;
	data?: unknown;
}

export interface JSONRPCEnvelope<TParams = unknown, TResult = unknown> {
	jsonrpc: '2.0';
	id?: string | number;
	method?: string;
	params?: TParams;
	result?: TResult;
	error?: RPCError;
}

export type Capability = 'reply' | 'react' | 'poke' | 'voice' | 'sticker' | 'multi_bubble';

export interface MediaRef {
	blob_id: string;
	mime_type: string;
	size_bytes?: number;
	duration_ms?: number;
	meta?: Record<string, unknown>;
}

export type BlockType =
	| 'text'
	| 'link'
	| 'image'
	| 'audio'
	| 'video'
	| 'forward_tree'
	| 'mention'
	| 'reply_ref';

export interface ForwardNode {
	sender_id?: string;
	sender_name?: string;
	timestamp_ms?: number;
	blocks?: ContentBlock[];
}

export interface ContentBlock {
	type: BlockType;
	text?: string;
	url?: string;
	title?: string;
	description?: string;
	media?: MediaRef;
	target_id?: string;
	target_name?: string;
	reply_to_msg_id?: string;
	forward_nodes?: ForwardNode[];
}

export interface InboundMessage {
	channel_id: string;
	room_id: string;
	msg_id: string;
	sender_id: string;
	sender_name: string;
	is_bot?: boolean;
	is_at_bot?: boolean;
	timestamp_ms: number;
	blocks: ContentBlock[];
	reply_to_msg_id?: string;
}

export type ActionType = 'send_text' | 'send_meme' | 'send_voice' | 'react' | 'poke';

export interface OutboundAction {
	type: ActionType;
	text?: string;
	reply_to_msg_id?: string;
	meme_ref?: string;
	media?: MediaRef;
	msg_id?: string;
	emoji?: string;
	user_id?: string;
}

export interface AdapterInfo {
	channel_id: string;
	kind: string;
	capabilities?: Capability[];
	connected?: boolean;
}

export interface UIEventParams<TPayload = unknown> {
	topic: UITopic | string;
	room_id?: string;
	timestamp_ms: number;
	payload?: TPayload;
}

export interface ChatSendParams {
	channel_id: string;
	room_id: string;
	text: string;
	reply_to_msg_id?: string;
	media?: MediaRef[];
}

export interface RunTriggerParams {
	room_id: string;
	source?: string;
	bypass_gate?: boolean;
	payload?: unknown;
}

export interface PADVec3 {
	p: number;
	a: number;
	d: number;
}

export interface PADState {
	value: PADVec3;
	feeling?: string;
	intensity?: number;
	updated_at: string;
}

export interface PADTemperament {
	initial_baseline: PADVec3;
	baseline: PADVec3;
	sensitivity: PADVec3;
	half_life_p_ms: number;
	half_life_a_ms: number;
	half_life_d_ms: number;
	deadzone_radius: number;
}

export type RunStage = 'idle' | 'gate' | 'assemble' | 'loop' | 'post_run' | 'done' | 'error';

export interface TraceStep {
	step_index: number;
	stage: RunStage;
	name: string;
	status: 'running' | 'ok' | 'skipped' | 'error';
	duration_ms?: number;
	tokens_in?: number;
	tokens_out?: number;
	cached_tokens?: number;
	detail?: Record<string, unknown>;
	timestamp_ms: number;
}

export interface TraceMemoryHit {
	id: string;
	content: string;
	score: number;
	source: 'fts' | 'vector' | 'assoc';
	t_event?: string;
}

export interface TraceToolCall {
	name: string;
	server?: string;
	args?: Record<string, unknown>;
	result_preview?: string;
	duration_ms?: number;
	is_error?: boolean;
}

export interface RunTraceSummary {
	run_id: string;
	room_id: string;
	source: string;
	status: 'running' | 'completed' | 'silent' | 'gated_out' | 'error';
	gate_score?: number;
	steps_count: number;
	duration_ms: number;
	tokens_in?: number;
	tokens_out?: number;
	cached_tokens?: number;
	started_at: string;
	summary?: string;
}

export interface RunTraceDetail extends RunTraceSummary {
	pad_before?: PADVec3;
	pad_after?: PADVec3;
	inner_feeling?: string;
	memory_hits?: TraceMemoryHit[];
	tool_calls?: TraceToolCall[];
	actions?: OutboundAction[];
	steps: TraceStep[];
	error?: string;
}

export interface DashboardMetrics {
	runs_today: number;
	replies_today: number;
	messages_total?: number;
	memories_total: number;
	memes_total: number;
	avg_latency_ms: number;
	cache_hit_ratio?: number;
}

export interface DashboardResponse {
	status: 'ok' | 'degraded';
	uptime_sec: number;
	pad: PADState;
	adapters: AdapterInfo[];
	metrics: DashboardMetrics;
	recent_traces: RunTraceSummary[];
	rooms?: string[];
}

export interface MemoryItem {
	id: string;
	room_id?: string;
	content: string;
	kind: 'fact' | 'episode' | 'preference' | 'relation';
	entities: string[];
	strength: number;
	recall_count: number;
	t_event?: string;
	superseded_by?: string;
	created_at: string;
	updated_at: string;
}

export interface MemorySearchResponse {
	query: string;
	items: MemoryItem[];
	elapsed_ms: number;
}

export interface DreamingStatus {
	last_run_at?: string;
	next_run_at?: string;
	pending_episodes: number;
	consolidated_count: number;
	running: boolean;
}

export interface PersonaTrait {
	id: string;
	text: string;
	weight: number;
	updated_at: string;
}

export interface PersonaSnapshot {
	version: number;
	created_at: string;
	reason: string;
	core_beliefs: string;
	traits: PersonaTrait[];
}

export interface PersonaResponse {
	core_beliefs: string;
	traits: PersonaTrait[];
	temperament: PADTemperament;
	pad: PADState;
	snapshots: PersonaSnapshot[];
}

export interface MemeItem {
	id: string;
	hash: string;
	blob_id: string;
	thumb_url: string;
	image_url: string;
	description: string;
	emotion_tags: string[];
	scene_tags: string[];
	use_count: number;
	blocked: boolean;
	created_at: string;
}

export interface MemeListResponse {
	items: MemeItem[];
	total: number;
}

export interface ProviderConfig {
	id: string;
	name: string;
	kind: string;
	base_url: string;
	has_api_key: boolean;
	api_key_hint?: string;
	enabled: boolean;
}

export interface ModelSlotConfig {
	slot: 'chat' | 'decision' | 'summary' | 'vision' | 'embedding' | 'tts' | 'stt';
	provider_id: string;
	model_id: string;
	capabilities?: {
		vision?: boolean;
		audio_in?: boolean;
		video_in?: boolean;
	};
}

export interface MCPServerConfig {
	id: string;
	name: string;
	transport: 'streamable_http' | 'stdio';
	endpoint?: string;
	command?: string;
	args?: string[];
	enabled: boolean;
	tools_count?: number;
	status?: 'connected' | 'disconnected' | 'error';
}

export interface BehaviorConfig {
	bot_name: string;
	bot_aliases: string;
	gate_threshold: number;
	gate_at_threshold: number;
	debounce_ms: number;
}

export interface RuntimeConfig {
	providers: ProviderConfig[];
	model_slots: ModelSlotConfig[];
	mcp_servers: MCPServerConfig[];
	features: {
		memory_enabled: boolean;
		emotion_enabled: boolean;
		meme_collect_enabled: boolean;
		voice_enabled?: boolean;
		bash_enabled: boolean;
	};
	behavior?: BehaviorConfig;
}

export interface ProviderUpdateInput {
	id?: string;
	name: string;
	kind: string;
	base_url: string;
	api_key?: string;
	enabled: boolean;
}

export interface RuntimeConfigPatch {
	providers?: ProviderUpdateInput[];
	model_slots?: ModelSlotConfig[];
	mcp_servers?: MCPServerConfig[];
	features?: Partial<RuntimeConfig['features']>;
	behavior?: Partial<BehaviorConfig>;
}

export interface TimelineMessage {
	msg_id: string;
	channel_id: string;
	room_id: string;
	sender_id: string;
	sender_name: string;
	is_bot: boolean;
	timestamp_ms: number;
	blocks: ContentBlock[];
	reply_to_msg_id?: string;
}
