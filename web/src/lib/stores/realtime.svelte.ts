import {
	JSONRPC_VERSION,
	RPC_METHODS,
	UI_TOPICS,
	type AdapterInfo,
	type JSONRPCEnvelope,
	type PADState,
	type PADVec3,
	type RunStage,
	type TimelineMessage,
	type TraceStep,
	type UIEventParams
} from '$lib/api';
import type { BadgeTone } from '$lib/components/ui';

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected' | 'reconnecting';

export interface ActiveRunState {
	run_id: string;
	room_id: string;
	stage: RunStage;
	steps: TraceStep[];
	updated_at: number;
}

export interface PADHistoryPoint {
	value: PADVec3;
	timestamp_ms: number;
}

interface PendingRPC {
	resolve: (result: unknown) => void;
	reject: (err: Error) => void;
	timer: ReturnType<typeof setTimeout>;
}

const MAX_RECENT_EVENTS = 50;
const MAX_PAD_HISTORY = 32;
const MAX_TIMELINE_LIVE = 100;
const BASE_RECONNECT_MS = 1000;
const MAX_RECONNECT_MS = 15000;
const DEFAULT_RPC_TIMEOUT_MS = 12000;

function deriveMoodTone(v: PADVec3): BadgeTone {
	const mag = Math.hypot(v.p, v.a, v.d);
	if (mag < 0.18) return 'neutral';
	if (v.p >= 0.2 && v.a >= 0.2) return 'coral';
	if (v.p >= 0.15) return 'mint';
	if (v.p <= -0.25 && v.a >= 0.2) return 'rose';
	if (v.p <= -0.2) return 'sky';
	if (v.d >= 0.25) return 'lilac';
	return 'amber';
}

function deriveMoodLabel(v: PADVec3, feeling?: string): string {
	if (feeling && feeling.trim()) return feeling.trim();
	const mag = Math.hypot(v.p, v.a, v.d);
	if (mag < 0.18) return '平静待机';
	if (v.p >= 0.2 && v.a >= 0.2) return '雀跃活跃';
	if (v.p >= 0.15) return '轻松愉悦';
	if (v.p <= -0.25 && v.a >= 0.2) return '有点紧绷';
	if (v.p <= -0.2) return '略显低落';
	if (v.a >= 0.25) return '好奇聚焦';
	return '随性漫游';
}

function buildWSUrl(): string {
	if (typeof window === 'undefined') return 'ws://127.0.0.1:8080/ws';
	const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
	return `${proto}//${window.location.host}/ws`;
}

class RealtimeStore {
	connectionStatus = $state<ConnectionStatus>('disconnected');
	lastError = $state<string | null>(null);
	padState = $state<PADState>({
		value: { p: 0.12, a: 0.02, d: 0.02 },
		feeling: '',
		updated_at: ''
	});
	padHistory = $state.raw<PADHistoryPoint[]>([]);
	activeRun = $state<ActiveRunState | null>(null);
	adapters = $state.raw<AdapterInfo[]>([]);
	recentEvents = $state.raw<UIEventParams[]>([]);
	liveTimeline = $state.raw<TimelineMessage[]>([]);
	configVersion = $state(0);

	moodTone = $derived<BadgeTone>(deriveMoodTone(this.padState.value));
	moodLabel = $derived<string>(deriveMoodLabel(this.padState.value, this.padState.feeling));

	private ws: WebSocket | null = null;
	private shouldReconnect = false;
	private reconnectAttempts = 0;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private reqSeq = 0;
	private pending = new Map<string, PendingRPC>();

	connect(): void {
		if (typeof window === 'undefined') return;
		if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
			return;
		}

		this.shouldReconnect = true;
		this.clearReconnectTimer();
		this.connectionStatus = this.reconnectAttempts > 0 ? 'reconnecting' : 'connecting';

		const socket = new WebSocket(buildWSUrl());
		this.ws = socket;

		socket.onopen = () => {
			if (this.ws !== socket) return;
			this.connectionStatus = 'connected';
			this.reconnectAttempts = 0;
			this.lastError = null;
			void this.subscribeAll();
		};

		socket.onmessage = (event) => {
			if (this.ws !== socket || typeof event.data !== 'string') return;
			this.handleFrame(event.data);
		};

		socket.onerror = () => {
			if (this.ws !== socket) return;
			this.lastError = 'WebSocket 连接异常';
		};

		socket.onclose = () => {
			if (this.ws !== socket) return;
			this.ws = null;
			this.rejectAllPending(new Error('websocket disconnected'));
			if (this.shouldReconnect) {
				this.connectionStatus = 'reconnecting';
				this.scheduleReconnect();
			} else {
				this.connectionStatus = 'disconnected';
			}
		};
	}

	disconnect(): void {
		this.shouldReconnect = false;
		this.clearReconnectTimer();
		this.rejectAllPending(new Error('websocket closed by client'));
		if (this.ws) {
			const socket = this.ws;
			this.ws = null;
			socket.close();
		}
		this.connectionStatus = 'disconnected';
	}

	seedPAD(state: PADState): void {
		this.padState = state;
	}

	seedAdapters(list: AdapterInfo[]): void {
		this.adapters = list;
	}

	callRPC<TResult = unknown>(
		method: string,
		params?: unknown,
		timeoutMs = DEFAULT_RPC_TIMEOUT_MS
	): Promise<TResult> {
		if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
			return Promise.reject(new Error('WebSocket 未连接'));
		}

		const id = `ui-${++this.reqSeq}`;
		const env: JSONRPCEnvelope = {
			jsonrpc: JSONRPC_VERSION,
			id,
			method,
			...(params !== undefined ? { params } : {})
		};

		return new Promise<TResult>((resolve, reject) => {
			const timer = setTimeout(() => {
				this.pending.delete(id);
				reject(new Error(`RPC ${method} 超时`));
			}, timeoutMs);

			this.pending.set(id, {
				resolve: (v) => resolve(v as TResult),
				reject,
				timer
			});

			try {
				this.ws?.send(JSON.stringify(env));
			} catch (err) {
				clearTimeout(timer);
				this.pending.delete(id);
				reject(err instanceof Error ? err : new Error(String(err)));
			}
		});
	}

	private async subscribeAll(): Promise<void> {
		try {
			await this.callRPC(RPC_METHODS.UI_SUBSCRIBE, {
				topics: [
					UI_TOPICS.TRACE_STEP,
					UI_TOPICS.PAD_UPDATE,
					UI_TOPICS.TIMELINE_APPEND,
					UI_TOPICS.CONFIG_UPDATED,
					UI_TOPICS.CHANNEL_STATUS
				]
			});
		} catch (err) {
			this.lastError = err instanceof Error ? err.message : '订阅实时事件失败';
		}
	}

	private handleFrame(raw: string): void {
		let env: JSONRPCEnvelope;
		try {
			env = JSON.parse(raw) as JSONRPCEnvelope;
		} catch {
			return;
		}

		if (env.jsonrpc !== JSONRPC_VERSION) return;

		if (!env.method && env.id !== undefined) {
			const key = String(env.id);
			const req = this.pending.get(key);
			if (!req) return;
			this.pending.delete(key);
			clearTimeout(req.timer);
			if (env.error) {
				req.reject(new Error(`RPC ${env.error.code}: ${env.error.message}`));
			} else {
				req.resolve(env.result);
			}
			return;
		}

		if (env.method === RPC_METHODS.UI_EVENT && env.params) {
			this.handleUIEvent(env.params as UIEventParams);
		}
	}

	private handleUIEvent(ev: UIEventParams): void {
		this.recentEvents = [ev, ...this.recentEvents].slice(0, MAX_RECENT_EVENTS);

		switch (ev.topic) {
			case UI_TOPICS.PAD_UPDATE:
				this.applyPADUpdate(ev.payload, ev.timestamp_ms);
				break;
			case UI_TOPICS.TRACE_STEP:
				this.applyTraceStep(ev.payload, ev.room_id, ev.timestamp_ms);
				break;
			case UI_TOPICS.TIMELINE_APPEND:
				this.applyTimelineAppend(ev.payload);
				break;
			case UI_TOPICS.CHANNEL_STATUS:
				this.applyChannelStatus(ev.payload);
				break;
			case UI_TOPICS.CONFIG_UPDATED:
				this.configVersion += 1;
				break;
		}
	}

	private applyPADUpdate(payload: unknown, timestampMs: number): void {
		if (!payload || typeof payload !== 'object') return;
		const p = payload as Partial<PADState> & Partial<PADVec3>;
		const vec: PADVec3 = p.value ?? {
			p: typeof p.p === 'number' ? p.p : this.padState.value.p,
			a: typeof p.a === 'number' ? p.a : this.padState.value.a,
			d: typeof p.d === 'number' ? p.d : this.padState.value.d
		};
		this.padState = {
			value: vec,
			feeling: typeof p.feeling === 'string' ? p.feeling : this.padState.feeling,
			intensity: typeof p.intensity === 'number' ? p.intensity : Math.hypot(vec.p, vec.a, vec.d),
			updated_at: p.updated_at ?? new Date(timestampMs || Date.now()).toISOString()
		};
		this.padHistory = [...this.padHistory, { value: vec, timestamp_ms: timestampMs || Date.now() }].slice(
			-MAX_PAD_HISTORY
		);
	}

	private applyTraceStep(payload: unknown, roomId: string | undefined, timestampMs: number): void {
		if (!payload || typeof payload !== 'object') return;
		const data = payload as {
			run_id?: string;
			room_id?: string;
			stage?: RunStage;
			step?: TraceStep;
		};
		const runId = data.run_id;
		if (!runId) return;

		const stage = data.stage ?? data.step?.stage ?? 'loop';
		const prevSteps = this.activeRun?.run_id === runId ? this.activeRun.steps : [];
		const nextSteps = data.step ? [...prevSteps, data.step] : prevSteps;

		this.activeRun = {
			run_id: runId,
			room_id: data.room_id ?? roomId ?? '',
			stage,
			steps: nextSteps,
			updated_at: timestampMs || Date.now()
		};
	}

	private applyTimelineAppend(payload: unknown): void {
		if (!payload || typeof payload !== 'object') return;
		const msg = payload as TimelineMessage;
		if (!msg.msg_id) return;
		this.liveTimeline = [...this.liveTimeline, msg].slice(-MAX_TIMELINE_LIVE);
	}

	private applyChannelStatus(payload: unknown): void {
		if (!payload || typeof payload !== 'object') return;
		const info = payload as AdapterInfo;
		if (!info.channel_id) return;

		if (info.connected === false) {
			this.adapters = this.adapters.filter((a) => a.channel_id !== info.channel_id);
			return;
		}
		const rest = this.adapters.filter((a) => a.channel_id !== info.channel_id);
		this.adapters = [...rest, { ...info, connected: true }].sort((a, b) =>
			a.channel_id.localeCompare(b.channel_id)
		);
	}

	private scheduleReconnect(): void {
		this.clearReconnectTimer();
		const delay = Math.min(BASE_RECONNECT_MS * 2 ** this.reconnectAttempts, MAX_RECONNECT_MS);
		this.reconnectAttempts += 1;
		this.reconnectTimer = setTimeout(() => {
			if (this.shouldReconnect) {
				this.connect();
			}
		}, delay);
	}

	private clearReconnectTimer(): void {
		if (this.reconnectTimer !== null) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
	}

	private rejectAllPending(err: Error): void {
		for (const req of this.pending.values()) {
			clearTimeout(req.timer);
			req.reject(err);
		}
		this.pending.clear();
	}
}

export const realtime = new RealtimeStore();
