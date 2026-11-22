import type {
	ChatSendParams,
	DashboardResponse,
	DreamingStatus,
	MediaRef,
	MemeItem,
	MemeListResponse,
	MemoryItem,
	MemorySearchResponse,
	PADTemperament,
	PersonaResponse,
	RunTraceDetail,
	RunTraceSummary,
	RunTriggerParams,
	RuntimeConfig,
	RuntimeConfigPatch,
	TimelineMessage
} from './types';

export class ApiError extends Error {
	readonly status: number;
	readonly body: unknown;

	constructor(status: number, message: string, body?: unknown) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
		this.body = body;
	}
}

export interface RequestOptions {
	signal?: AbortSignal;
}

async function requestJSON<T>(
	path: string,
	init: RequestInit = {},
	opts: RequestOptions = {}
): Promise<T> {
	const headers = new Headers(init.headers);
	if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
		headers.set('Content-Type', 'application/json');
	}

	const res = await fetch(path, {
		...init,
		headers,
		signal: opts.signal
	});

	const contentType = res.headers.get('content-type') ?? '';
	const isJSON = contentType.includes('application/json');
	const payload = isJSON ? await res.json().catch(() => null) : await res.text().catch(() => '');

	if (!res.ok) {
		const msg =
			typeof payload === 'object' && payload !== null && 'error' in payload
				? String((payload as { error: unknown }).error)
				: typeof payload === 'string' && payload.trim()
					? payload.trim()
					: `HTTP ${res.status}`;
		throw new ApiError(res.status, msg, payload);
	}

	return payload as T;
}

function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
	const qs = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) {
		if (v !== undefined && v !== '') {
			qs.set(k, String(v));
		}
	}
	const encoded = qs.toString();
	return encoded ? `?${encoded}` : '';
}

export const api = {
	getDashboard(opts?: RequestOptions): Promise<DashboardResponse> {
		return requestJSON<DashboardResponse>('/api/dashboard', {}, opts);
	},

	listTraces(
		params: { room_id?: string; status?: string; limit?: number } = {},
		opts?: RequestOptions
	): Promise<{ items: RunTraceSummary[] }> {
		return requestJSON<{ items: RunTraceSummary[] }>(
			`/api/traces${buildQuery(params)}`,
			{},
			opts
		);
	},

	getTrace(runId: string, opts?: RequestOptions): Promise<RunTraceDetail> {
		return requestJSON<RunTraceDetail>(`/api/traces/${encodeURIComponent(runId)}`, {}, opts);
	},

	listMemories(
		params: { room_id?: string; kind?: string; limit?: number } = {},
		opts?: RequestOptions
	): Promise<{ items: MemoryItem[]; dreaming?: DreamingStatus }> {
		return requestJSON<{ items: MemoryItem[]; dreaming?: DreamingStatus }>(
			`/api/memories${buildQuery(params)}`,
			{},
			opts
		);
	},

	searchMemories(
		query: string,
		params: { room_id?: string; limit?: number } = {},
		opts?: RequestOptions
	): Promise<MemorySearchResponse> {
		return requestJSON<MemorySearchResponse>(
			`/api/memories/search${buildQuery({ q: query, ...params })}`,
			{},
			opts
		);
	},

	triggerDreaming(opts?: RequestOptions): Promise<{ accepted: boolean }> {
		return requestJSON<{ accepted: boolean }>('/api/memories/dream', { method: 'POST' }, opts);
	},

	getPersona(opts?: RequestOptions): Promise<PersonaResponse> {
		return requestJSON<PersonaResponse>('/api/persona', {}, opts);
	},

	updatePersonaBeliefs(coreBeliefs: string, opts?: RequestOptions): Promise<PersonaResponse> {
		return requestJSON<PersonaResponse>(
			'/api/persona',
			{ method: 'PATCH', body: JSON.stringify({ core_beliefs: coreBeliefs }) },
			opts
		);
	},

	updateTemperament(temperament: PADTemperament, opts?: RequestOptions): Promise<PersonaResponse> {
		return requestJSON<PersonaResponse>(
			'/api/persona/temperament',
			{ method: 'PUT', body: JSON.stringify(temperament) },
			opts
		);
	},

	listMemes(
		params: { q?: string; tag?: string; blocked?: boolean; limit?: number } = {},
		opts?: RequestOptions
	): Promise<MemeListResponse> {
		return requestJSON<MemeListResponse>(`/api/memes${buildQuery(params)}`, {}, opts);
	},

	updateMeme(
		id: string,
		patch: Partial<Pick<MemeItem, 'description' | 'emotion_tags' | 'scene_tags' | 'blocked'>>,
		opts?: RequestOptions
	): Promise<MemeItem> {
		return requestJSON<MemeItem>(
			`/api/memes/${encodeURIComponent(id)}`,
			{ method: 'PATCH', body: JSON.stringify(patch) },
			opts
		);
	},

	deleteMeme(id: string, opts?: RequestOptions): Promise<{ deleted: boolean }> {
		return requestJSON<{ deleted: boolean }>(
			`/api/memes/${encodeURIComponent(id)}`,
			{ method: 'DELETE' },
			opts
		);
	},

	getConfig(opts?: RequestOptions): Promise<RuntimeConfig> {
		return requestJSON<RuntimeConfig>('/api/config', {}, opts);
	},

	updateConfig(patch: RuntimeConfigPatch, opts?: RequestOptions): Promise<RuntimeConfig> {
		return requestJSON<RuntimeConfig>(
			'/api/config',
			{ method: 'PATCH', body: JSON.stringify(patch) },
			opts
		);
	},

	getTimeline(
		params: { room_id?: string; limit?: number } = {},
		opts?: RequestOptions
	): Promise<{ items: TimelineMessage[] }> {
		return requestJSON<{ items: TimelineMessage[] }>(
			`/api/chat/timeline${buildQuery(params)}`,
			{},
			opts
		);
	},

	sendChat(params: ChatSendParams, opts?: RequestOptions): Promise<{ accepted: boolean }> {
		return requestJSON<{ accepted: boolean }>(
			'/api/chat/send',
			{ method: 'POST', body: JSON.stringify(params) },
			opts
		);
	},

	triggerRun(params: RunTriggerParams, opts?: RequestOptions): Promise<{ accepted: boolean }> {
		return requestJSON<{ accepted: boolean }>(
			'/api/runs/trigger',
			{ method: 'POST', body: JSON.stringify(params) },
			opts
		);
	},

	async uploadBlob(file: File | Blob, opts?: RequestOptions): Promise<MediaRef> {
		const form = new FormData();
		form.append('file', file);
		return requestJSON<MediaRef>('/api/blobs', { method: 'POST', body: form }, opts);
	},

	blobUrl(blobId: string): string {
		return `/api/blobs/${encodeURIComponent(blobId)}`;
	}
};
