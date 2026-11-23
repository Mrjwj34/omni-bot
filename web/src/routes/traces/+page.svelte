<script lang="ts">
	import { page } from '$app/state';
	import {
		Activity,
		AlertTriangle,
		Brain,
		ChevronDown,
		ChevronRight,
		Clock,
		Cpu,
		Hand,
		Image as ImageIcon,
		Layers,
		MessageSquare,
		Mic,
		RefreshCw,
		Reply,
		Search,
		Send,
		Smile,
		Sparkles,
		Wrench,
		Zap
	} from '@lucide/svelte';
	import {
		api,
		type OutboundAction,
		type PADVec3,
		type RunTraceDetail,
		type RunTraceSummary,
		type TraceStep,
		type TraceToolCall
	} from '$lib/api';
	import {
		Badge,
		Button,
		EmptyState,
		Input,
		PageHeader,
		SurfaceCard,
		Tabs,
		cn,
		type BadgeTone,
		type TabItem
	} from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	type StatusFilter = 'all' | 'replied' | 'silent' | 'skipped' | 'error';

	let statusFilter = $state<StatusFilter>('all');
	let searchQuery = $state('');
	let traces = $state.raw<RunTraceSummary[]>([]);
	let loadingList = $state(false);
	let selectedRunId = $state<string | null>(null);
	let lastUrlRun: string | null = null;
	let detail = $state<RunTraceDetail | null>(null);
	let loadingDetail = $state(false);
	let detailTab = $state('overview');
	let expandedTools = $state<Record<string, boolean>>({});

	const statusPills: { key: StatusFilter; label: string }[] = [
		{ key: 'all', label: '全部' },
		{ key: 'replied', label: '已回复' },
		{ key: 'silent', label: '静音' },
		{ key: 'skipped', label: '跳过' },
		{ key: 'error', label: '异常' }
	];

	function normalizeStatus(status: string): Exclude<StatusFilter, 'all'> | 'running' {
		switch (status) {
			case 'completed':
			case 'replied':
				return 'replied';
			case 'silent':
				return 'silent';
			case 'gated_out':
			case 'skipped':
				return 'skipped';
			case 'error':
				return 'error';
			case 'running':
				return 'running';
			default:
				return 'skipped';
		}
	}

	function statusMeta(status: string): { label: string; tone: BadgeTone } {
		switch (normalizeStatus(status)) {
			case 'replied':
				return { label: '已回复', tone: 'mint' };
			case 'silent':
				return { label: '静音', tone: 'amber' };
			case 'skipped':
				return { label: '跳过', tone: 'neutral' };
			case 'running':
				return { label: '运行中', tone: 'sky' };
			case 'error':
				return { label: '异常', tone: 'rose' };
		}
	}

	let mergedTraces = $derived.by<RunTraceSummary[]>(() => {
		const live = realtime.activeRun;
		if (!live || traces.some((t) => t.run_id === live.run_id)) {
			return traces;
		}
		const liveSummary: RunTraceSummary = {
			run_id: live.run_id,
			room_id: live.room_id || 'default',
			source: 'live',
			status: live.stage === 'error' ? 'error' : live.stage === 'done' ? 'completed' : 'running',
			steps_count: live.steps.length,
			duration_ms: Math.max(0, Date.now() - live.updated_at),
			started_at: new Date(live.updated_at).toISOString()
		};
		return [liveSummary, ...traces];
	});

	let filteredTraces = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		return mergedTraces.filter((item) => {
			if (statusFilter !== 'all') {
				const norm = normalizeStatus(item.status);
				if (norm !== statusFilter) return false;
			}
			if (q) {
				const haystack = `${item.run_id} ${item.room_id} ${item.source} ${item.summary ?? ''}`.toLowerCase();
				if (!haystack.includes(q)) return false;
			}
			return true;
		});
	});

	let activeSummary = $derived(
		mergedTraces.find((t) => t.run_id === selectedRunId) ?? filteredTraces[0] ?? null
	);

	let activeDetail = $derived.by<RunTraceDetail | null>(() => {
		if (detail && detail.run_id === activeSummary?.run_id) {
			return detail;
		}
		if (!activeSummary) return null;
		const live = realtime.activeRun;
		const liveSteps = live && live.run_id === activeSummary.run_id ? live.steps : [];
		return {
			...activeSummary,
			steps: liveSteps
		};
	});

	let detailTabs = $derived<TabItem[]>([
		{
			value: 'overview',
			label: 'Gate & 记忆',
			badge: activeDetail?.memory_hits?.length
		},
		{
			value: 'steps',
			label: 'Agent Loop',
			badge: activeDetail?.steps?.length ?? activeDetail?.steps_count
		},
		{
			value: 'actions',
			label: '出站动作',
			badge: activeDetail?.actions?.length
		}
	]);

	async function loadTraces(signal?: AbortSignal): Promise<void> {
		loadingList = true;
		try {
			const res = await api.listTraces({ limit: 50 }, { signal });
			traces = res.items ?? [];
			if (!selectedRunId) {
				const urlRun = page.url.searchParams.get('run');
				if (urlRun) {
					lastUrlRun = urlRun;
					selectedRunId = urlRun;
				} else if (traces.length > 0) {
					selectedRunId = traces[0].run_id;
				}
			}
		} catch {
			// Standalone offline fallback keeps UI clean.
		} finally {
			loadingList = false;
		}
	}

	async function loadDetail(runId: string, signal?: AbortSignal): Promise<void> {
		loadingDetail = true;
		try {
			detail = await api.getTrace(runId, { signal });
		} catch {
			if (detail?.run_id !== runId) {
				detail = null;
			}
		} finally {
			loadingDetail = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void loadTraces(ctrl.signal);
		return () => ctrl.abort();
	});

	$effect(() => {
		const urlRun = page.url.searchParams.get('run');
		if (urlRun && urlRun !== lastUrlRun) {
			lastUrlRun = urlRun;
			selectedRunId = urlRun;
		}
	});

	let lastLiveFinishedId: string | null = null;
	$effect(() => {
		const live = realtime.activeRun;
		if (!live) return;
		if ((live.stage === 'done' || live.stage === 'error') && live.run_id !== lastLiveFinishedId) {
			lastLiveFinishedId = live.run_id;
			void loadTraces();
		}
	});

	$effect(() => {
		const id = activeSummary?.run_id;
		if (!id) {
			detail = null;
			return;
		}
		const ctrl = new AbortController();
		void loadDetail(id, ctrl.signal);
		return () => ctrl.abort();
	});

	function selectTrace(runId: string): void {
		selectedRunId = runId;
		expandedTools = {};
	}

	function toggleTool(key: string): void {
		expandedTools = { ...expandedTools, [key]: !expandedTools[key] };
	}

	function fmtSigned(v: number): string {
		const sign = v > 0 ? '+' : '';
		return `${sign}${v.toFixed(2)}`;
	}

	function fmtTime(iso: string): string {
		if (!iso) return '--:--:--';
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return iso;
		return d.toLocaleTimeString('zh-CN', { hour12: false });
	}

	function fmtDateTag(raw?: string): string {
		if (!raw) return '';
		if (/^\d{4}-\d{2}-\d{2}/.test(raw)) return raw.slice(0, 10);
		const d = new Date(raw);
		if (Number.isNaN(d.getTime())) return raw;
		return d.toISOString().slice(0, 10);
	}

	function padDelta(before?: PADVec3, after?: PADVec3): { key: 'P' | 'A' | 'D'; before: number; after: number; delta: number }[] {
		if (!before && !after) return [];
		const b = before ?? { p: 0, a: 0, d: 0 };
		const a = after ?? b;
		return [
			{ key: 'P', before: b.p, after: a.p, delta: a.p - b.p },
			{ key: 'A', before: b.a, after: a.a, delta: a.a - b.a },
			{ key: 'D', before: b.d, after: a.d, delta: a.d - b.d }
		];
	}

	function readString(obj: Record<string, unknown> | undefined, ...keys: string[]): string | undefined {
		if (!obj) return undefined;
		for (const k of keys) {
			const val = obj[k];
			if (typeof val === 'string' && val.trim()) return val.trim();
		}
		return undefined;
	}

	function readNumber(obj: Record<string, unknown> | undefined, ...keys: string[]): number | undefined {
		if (!obj) return undefined;
		for (const k of keys) {
			const val = obj[k];
			if (typeof val === 'number' && !Number.isNaN(val)) return val;
		}
		return undefined;
	}

	function extractStepToolCalls(step: TraceStep): TraceToolCall[] {
		const raw = step.detail?.tool_calls;
		if (!Array.isArray(raw)) return [];
		return raw.filter((item): item is TraceToolCall => Boolean(item && typeof item === 'object' && 'name' in item));
	}

	function formatJSON(val: unknown): string {
		try {
			return JSON.stringify(val, null, 2);
		} catch {
			return String(val);
		}
	}

	function actionMeta(action: OutboundAction): { label: string; tone: BadgeTone } {
		switch (action.type) {
			case 'send_text':
				return { label: '文本气泡', tone: 'mint' };
			case 'send_meme':
				return { label: '表情包', tone: 'amber' };
			case 'send_voice':
				return { label: '语音合成', tone: 'sky' };
			case 'react':
				return { label: '回应贴纸', tone: 'lilac' };
			case 'poke':
				return { label: '戳一戳', tone: 'coral' };
		}
	}

	function memorySourceTone(source: string): BadgeTone {
		switch (source) {
			case 'fts':
				return 'sky';
			case 'vector':
				return 'lilac';
			case 'assoc':
				return 'mint';
			default:
				return 'neutral';
		}
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="Run 轨迹">
		{#snippet badge()}
			<Badge tone="sky" size="sm">{filteredTraces.length} 条</Badge>
			{#if realtime.activeRun && realtime.activeRun.stage !== 'done' && realtime.activeRun.stage !== 'idle'}
				<Badge tone="mint" size="sm" dot pulse>实时追踪中</Badge>
			{/if}
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" loading={loadingList} onclick={() => void loadTraces()}>
				<RefreshCw class={cn('size-3.5', loadingList && 'animate-spin')} />
				<span>刷新</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Filter Bar -->
	<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
		<div class="flex flex-wrap items-center gap-1.5">
			{#each statusPills as pill (pill.key)}
				{@const active = statusFilter === pill.key}
				<button
					type="button"
					onclick={() => (statusFilter = pill.key)}
					class={cn(
						'cursor-pointer rounded-[var(--radius-pill)] px-3.5 py-1.5 text-xs font-medium whitespace-nowrap transition-all duration-150',
						active
							? 'bg-ink-primary text-white shadow-[var(--shadow-xs)]'
							: 'bg-surface-raised text-ink-secondary shadow-[var(--shadow-xs)] hover:bg-surface-subtle hover:text-ink-primary'
					)}
				>
					{pill.label}
				</button>
			{/each}
		</div>

		<Input bind:value={searchQuery} placeholder="筛选房间 / Run ID / 触发源..." class="sm:max-w-xs">
			{#snippet leading()}
				<Search class="size-4" />
			{/snippet}
		</Input>
	</div>

	{#if filteredTraces.length === 0}
		<EmptyState title="暂无匹配的 Run 轨迹" hint="群聊消息触发或调试台发送后将在此实时记录" tone="sky">
			{#snippet icon()}
				<Activity class="size-5" />
			{/snippet}
			{#snippet action()}
				<Button variant="primary" size="sm" href="/chat">前往对话调试</Button>
			{/snippet}
		</EmptyState>
	{:else}
		<!-- Master-Detail Responsive Grid -->
		<div class="grid gap-4 lg:grid-cols-12">
			<!-- Trace List (Master) -->
			<SurfaceCard variant="raised" padding="sm" class="flex flex-col gap-2 lg:col-span-5 lg:max-h-[760px] lg:overflow-y-auto">
				{#each filteredTraces as item (item.run_id)}
					{@const st = statusMeta(item.status)}
					{@const selected = activeSummary?.run_id === item.run_id}
					{@const totalTok = (item.tokens_in ?? 0) + (item.tokens_out ?? 0)}
					<button
						type="button"
						onclick={() => selectTrace(item.run_id)}
						class={cn(
							'flex w-full cursor-pointer flex-col gap-2 rounded-[var(--radius-panel)] p-3.5 text-left transition-all duration-150',
							selected
								? 'bg-tint-mint shadow-[var(--shadow-xs)]'
								: 'bg-surface-subtle/80 hover:bg-surface-muted/70'
						)}
					>
						<div class="flex items-center justify-between gap-2">
							<div class="flex min-w-0 items-center gap-1.5">
								<Badge tone={st.tone} size="sm" dot={item.status === 'running'} pulse={item.status === 'running'}>
									{st.label}
								</Badge>
								<span class="truncate rounded-full bg-surface-raised/90 px-2 py-0.5 text-[11px] font-medium text-ink-secondary">
									{item.source}
								</span>
							</div>
							<span class="shrink-0 font-mono text-[11px] text-ink-muted">
								{fmtTime(item.started_at)}
							</span>
						</div>

						<div class="flex items-center justify-between gap-2">
							<span class="truncate font-mono text-xs font-semibold text-ink-primary">
								{item.room_id || 'default'} · {item.run_id}
							</span>
						</div>

						{#if item.summary}
							<p class="line-clamp-1 text-xs text-ink-secondary">{item.summary}</p>
						{/if}

						<div class="flex flex-wrap items-center gap-2 font-mono text-[11px] text-ink-muted">
							<span class="inline-flex items-center gap-1 rounded-full bg-surface-raised/80 px-2 py-0.5 text-ink-secondary">
								<Clock class="size-3" />
								{Math.round(item.duration_ms)}ms
							</span>
							{#if totalTok > 0}
								<span class="inline-flex items-center gap-1 rounded-full bg-surface-raised/80 px-2 py-0.5 text-ink-secondary">
									<Cpu class="size-3" />
									{totalTok} tok
								</span>
							{/if}
							{#if item.gate_score !== undefined}
								<span class="inline-flex items-center gap-1 rounded-full bg-surface-raised/80 px-2 py-0.5 text-ink-secondary">
									Gate {Math.round(item.gate_score * 100)}%
								</span>
							{/if}
							<span class="ml-auto">{item.steps_count} 步</span>
						</div>
					</button>
				{/each}
			</SurfaceCard>

			<!-- Detail Inspector -->
			<SurfaceCard variant="raised" padding="lg" class="flex flex-col gap-4 lg:col-span-7">
				{#if !activeDetail}
					<div class="flex flex-1 items-center justify-center py-16 text-xs text-ink-muted">
						选择左侧轨迹查看明细
					</div>
				{:else}
					{@const st = statusMeta(activeDetail.status)}
					{@const deltas = padDelta(activeDetail.pad_before, activeDetail.pad_after)}
					{@const totalTok = (activeDetail.tokens_in ?? 0) + (activeDetail.tokens_out ?? 0)}

					<!-- Detail Header -->
					<div class="flex flex-wrap items-start justify-between gap-3">
						<div class="min-w-0 space-y-1">
							<div class="flex flex-wrap items-center gap-2">
								<Badge tone={st.tone} size="sm" dot={activeDetail.status === 'running'} pulse={activeDetail.status === 'running'}>
									{st.label}
								</Badge>
								<Badge tone="sky" size="sm">{activeDetail.source}</Badge>
								<span class="truncate font-mono text-xs font-semibold text-ink-primary">
									{activeDetail.run_id}
								</span>
							</div>
							<div class="flex flex-wrap items-center gap-3 font-mono text-xs text-ink-muted">
								<span>房间 {activeDetail.room_id || 'default'}</span>
								<span>{Math.round(activeDetail.duration_ms)}ms</span>
								{#if totalTok > 0}
									<span>
										in {activeDetail.tokens_in ?? 0} / out {activeDetail.tokens_out ?? 0}
										{#if activeDetail.cached_tokens}
											· 缓存 {activeDetail.cached_tokens}
										{/if}
									</span>
								{/if}
							</div>
						</div>

						{#if loadingDetail}
							<Badge tone="neutral" size="sm">加载中...</Badge>
						{/if}
					</div>

					{#if activeDetail.error}
						<div class="flex items-start gap-2.5 rounded-[var(--radius-panel)] bg-tint-rose p-3.5 text-xs text-rose-600">
							<AlertTriangle class="mt-0.5 size-4 shrink-0" />
							<span class="break-all font-mono">{activeDetail.error}</span>
						</div>
					{/if}

					<!-- Structured Detail Tabs -->
					<Tabs bind:value={detailTab} items={detailTabs}>
						{#snippet children(tab)}
							{#if tab === 'overview'}
								<div class="space-y-4">
									<!-- Gate & PAD Delta Section -->
									<div class="rounded-[var(--radius-panel)] bg-surface-subtle p-4 space-y-3.5">
										<div class="flex flex-wrap items-center justify-between gap-2">
											<div class="flex items-center gap-2 text-xs font-semibold text-ink-primary">
												<Zap class="size-4 text-sky-600" />
												<span>门控与情绪跃迁</span>
											</div>
											{#if activeDetail.gate_score !== undefined}
												<Badge
													tone={activeDetail.gate_score >= 0.5 ? 'mint' : 'amber'}
													size="sm"
													class="font-mono"
												>
													Gate {(activeDetail.gate_score * 100).toFixed(0)}%
												</Badge>
											{/if}
										</div>

										{#if activeDetail.gate_score !== undefined}
											<div class="h-2 w-full overflow-hidden rounded-full bg-surface-raised">
												<div
													class="h-full rounded-full bg-gradient-to-r from-sky-400 to-mint-500 transition-all duration-300"
													style:width={`${Math.max(0, Math.min(100, activeDetail.gate_score * 100))}%`}
												></div>
											</div>
										{/if}

										{#if activeDetail.inner_feeling}
											<div class="rounded-[var(--radius-control)] bg-surface-raised px-3.5 py-2.5 text-xs text-ink-primary">
												<span class="mr-2 font-mono text-[10px] font-semibold text-ink-muted">&lt;inner_feeling&gt;</span>
												<span>“{activeDetail.inner_feeling}”</span>
											</div>
										{/if}

										{#if deltas.length > 0}
											<div class="grid grid-cols-3 gap-2">
												{#each deltas as d (d.key)}
													{@const deltaTone =
														d.delta > 0.01 ? 'mint' : d.delta < -0.01 ? 'rose' : 'neutral'}
													<div class="rounded-[var(--radius-control)] bg-surface-raised p-2.5 text-center">
														<div class="flex items-center justify-center gap-1.5">
															<span class="font-mono text-xs font-bold text-ink-primary">{d.key}</span>
															<Badge tone={deltaTone} size="sm" class="px-1.5 py-0 font-mono text-[10px]">
																{fmtSigned(d.delta)}
															</Badge>
														</div>
														<div class="mt-1 font-mono text-[11px] text-ink-muted">
															{fmtSigned(d.before)} → {fmtSigned(d.after)}
														</div>
													</div>
												{/each}
											</div>
										{:else}
											<div class="text-xs text-ink-muted">无 PAD 变更采样</div>
										{/if}
									</div>

									<!-- Passive Memory Hits -->
									<div class="space-y-2.5">
										<div class="flex items-center justify-between">
											<div class="flex items-center gap-2 text-xs font-semibold text-ink-primary">
												<Brain class="size-4 text-lilac-600" />
												<span>被动联想命中</span>
											</div>
											<Badge tone="lilac" size="sm">
												{activeDetail.memory_hits?.length ?? 0} 条
											</Badge>
										</div>

										{#if !activeDetail.memory_hits || activeDetail.memory_hits.length === 0}
											<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-6 text-center text-xs text-ink-muted">
												本轮未注入联想记忆
											</div>
										{:else}
											<ul class="space-y-2">
												{#each activeDetail.memory_hits as hit (hit.id)}
													<li class="rounded-[var(--radius-panel)] bg-surface-subtle p-3.5 space-y-2">
														<div class="flex flex-wrap items-center justify-between gap-2">
															<div class="flex items-center gap-1.5">
																<Badge tone={memorySourceTone(hit.source)} size="sm">
																	{hit.source.toUpperCase()}
																</Badge>
																{#if hit.t_event}
																	<span class="rounded-full bg-surface-raised px-2 py-0.5 font-mono text-[11px] text-ink-secondary">
																		[{fmtDateTag(hit.t_event)}]
																	</span>
																{/if}
															</div>
															<div class="flex items-center gap-2">
																<div class="h-1.5 w-16 overflow-hidden rounded-full bg-surface-raised">
																	<div
																		class="h-full rounded-full bg-lilac-500"
																		style:width={`${Math.max(8, Math.min(100, hit.score * 100))}%`}
																	></div>
																</div>
																<span class="font-mono text-[11px] font-semibold text-lilac-600">
																	{hit.score.toFixed(2)}
																</span>
															</div>
														</div>
														<p class="text-xs leading-relaxed text-ink-primary">{hit.content}</p>
													</li>
												{/each}
											</ul>
										{/if}
									</div>
								</div>
							{:else if tab === 'steps'}
								<div class="space-y-3">
									{#if (!activeDetail.steps || activeDetail.steps.length === 0) && (!activeDetail.tool_calls || activeDetail.tool_calls.length === 0)}
										<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
											暂无步进记录
										</div>
									{:else}
										{#each activeDetail.steps as step, idx (`${step.stage}-${step.step_index}-${idx}`)}
											{@const model = readString(step.detail, 'model', 'model_id')}
											{@const thoughtTokens = readNumber(
												step.detail,
												'thought_tokens',
												'reasoning_tokens'
											)}
											{@const stepTools = extractStepToolCalls(step)}
											<div class="rounded-[var(--radius-panel)] bg-surface-subtle p-3.5 space-y-2.5">
												<div class="flex flex-wrap items-center justify-between gap-2">
													<div class="flex min-w-0 items-center gap-2">
														<span class="rounded-lg bg-surface-raised px-2 py-0.5 font-mono text-[11px] font-bold text-ink-secondary">
															#{String(step.step_index || idx + 1).padStart(2, '0')}
														</span>
														<Badge
															tone={step.status === 'error'
																? 'rose'
																: step.status === 'running'
																	? 'sky'
																	: step.status === 'skipped'
																		? 'neutral'
																		: 'mint'}
															size="sm"
														>
															{step.stage}
														</Badge>
														<span class="truncate text-xs font-semibold text-ink-primary">
															{step.name}
														</span>
													</div>

													<div class="flex flex-wrap items-center gap-1.5 font-mono text-[11px] text-ink-muted">
														{#if model}
															<span class="rounded-full bg-tint-sky px-2 py-0.5 text-sky-600">
																{model}
															</span>
														{/if}
														{#if step.duration_ms !== undefined}
															<span class="rounded-full bg-surface-raised px-2 py-0.5 text-ink-secondary">
																{Math.round(step.duration_ms)}ms
															</span>
														{/if}
													</div>
												</div>

												{#if step.tokens_in || step.tokens_out || step.cached_tokens || thoughtTokens}
													<div class="flex flex-wrap items-center gap-1.5 font-mono text-[11px]">
														{#if step.tokens_in !== undefined}
															<span class="rounded-full bg-surface-raised px-2.5 py-0.5 text-ink-secondary">
																in {step.tokens_in}
															</span>
														{/if}
														{#if step.tokens_out !== undefined}
															<span class="rounded-full bg-surface-raised px-2.5 py-0.5 text-ink-secondary">
																out {step.tokens_out}
															</span>
														{/if}
														{#if step.cached_tokens}
															<span class="rounded-full bg-tint-mint px-2.5 py-0.5 text-mint-700">
																cached {step.cached_tokens}
															</span>
														{/if}
														{#if thoughtTokens}
															<span class="rounded-full bg-tint-lilac px-2.5 py-0.5 text-lilac-600">
																thought {thoughtTokens}
															</span>
														{/if}
													</div>
												{/if}

												{#if stepTools.length > 0}
													<div class="space-y-1.5 pt-1">
														{#each stepTools as tc, tIdx (`step-${idx}-tool-${tIdx}`)}
															{@const tKey = `s-${idx}-${tIdx}`}
															{@const open = Boolean(expandedTools[tKey])}
															<div class="rounded-[var(--radius-control)] bg-surface-raised">
																<button
																	type="button"
																	onclick={() => toggleTool(tKey)}
																	class="flex w-full cursor-pointer items-center justify-between gap-2 px-3 py-2 text-left text-xs"
																>
																	<div class="flex min-w-0 items-center gap-2">
																		{#if open}
																			<ChevronDown class="size-3.5 shrink-0 text-ink-muted" />
																		{:else}
																			<ChevronRight class="size-3.5 shrink-0 text-ink-muted" />
																		{/if}
																		<Wrench class="size-3.5 shrink-0 text-mint-600" />
																		<span class="truncate font-mono font-semibold text-ink-primary">
																			{tc.name}
																		</span>
																		{#if tc.server}
																			<Badge tone="lilac" size="sm">MCP:{tc.server}</Badge>
																		{/if}
																		{#if tc.is_error}
																			<Badge tone="rose" size="sm">ERR</Badge>
																		{/if}
																	</div>
																	{#if tc.duration_ms !== undefined}
																		<span class="shrink-0 font-mono text-[11px] text-ink-muted">
																			{Math.round(tc.duration_ms)}ms
																		</span>
																	{/if}
																</button>
																{#if open}
																	<div class="space-y-2 px-3 pb-3">
																		{#if tc.args}
																			<pre class="overflow-x-auto rounded-xl bg-surface-subtle p-2.5 font-mono text-[11px] text-ink-secondary">{formatJSON(tc.args)}</pre>
																		{/if}
																		{#if tc.result_preview}
																			<pre class="overflow-x-auto rounded-xl bg-tint-mint/50 p-2.5 font-mono text-[11px] text-ink-primary whitespace-pre-wrap break-all">{tc.result_preview}</pre>
																		{/if}
																	</div>
																{/if}
															</div>
														{/each}
													</div>
												{/if}
											</div>
										{/each}

										<!-- Top-level Tool / MCP Calls summary if provided directly on RunTraceDetail -->
										{#if activeDetail.tool_calls && activeDetail.tool_calls.length > 0}
											<div class="space-y-2 pt-1">
												<div class="flex items-center gap-2 text-xs font-semibold text-ink-secondary">
													<Layers class="size-3.5 text-mint-600" />
													<span>工具 / MCP 调用明细</span>
													<Badge tone="neutral" size="sm">{activeDetail.tool_calls.length}</Badge>
												</div>
												{#each activeDetail.tool_calls as tc, idx (`top-tool-${idx}-${tc.name}`)}
													{@const tKey = `top-${idx}`}
													{@const open = Boolean(expandedTools[tKey])}
													<div class="rounded-[var(--radius-panel)] bg-surface-subtle">
														<button
															type="button"
															onclick={() => toggleTool(tKey)}
															class="flex w-full cursor-pointer items-center justify-between gap-2 px-3.5 py-2.5 text-left text-xs"
														>
															<div class="flex min-w-0 items-center gap-2">
																{#if open}
																	<ChevronDown class="size-3.5 shrink-0 text-ink-muted" />
																{:else}
																	<ChevronRight class="size-3.5 shrink-0 text-ink-muted" />
																{/if}
																<Wrench class="size-3.5 shrink-0 text-mint-600" />
																<span class="truncate font-mono font-semibold text-ink-primary">
																	{tc.name}
																</span>
																{#if tc.server}
																	<Badge tone="lilac" size="sm">MCP:{tc.server}</Badge>
																{/if}
																{#if tc.is_error}
																	<Badge tone="rose" size="sm">错误</Badge>
																{/if}
															</div>
															{#if tc.duration_ms !== undefined}
																<span class="shrink-0 font-mono text-[11px] text-ink-muted">
																	{Math.round(tc.duration_ms)}ms
																</span>
															{/if}
														</button>
														{#if open}
															<div class="space-y-2 px-3.5 pb-3.5">
																{#if tc.args}
																	<div class="space-y-1">
																		<div class="font-mono text-[10px] font-semibold text-ink-muted">ARGS</div>
																		<pre class="overflow-x-auto rounded-xl bg-surface-raised p-2.5 font-mono text-[11px] text-ink-secondary">{formatJSON(tc.args)}</pre>
																	</div>
																{/if}
																{#if tc.result_preview}
																	<div class="space-y-1">
																		<div class="font-mono text-[10px] font-semibold text-ink-muted">RESULT</div>
																		<pre class="overflow-x-auto rounded-xl bg-surface-raised p-2.5 font-mono text-[11px] text-ink-primary whitespace-pre-wrap break-all">{tc.result_preview}</pre>
																	</div>
																{/if}
															</div>
														{/if}
													</div>
												{/each}
											</div>
										{/if}
									{/if}
								</div>
							{:else}
								<!-- Outbound Actions IR -->
								<div class="space-y-2.5">
									{#if !activeDetail.actions || activeDetail.actions.length === 0}
										<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
											本轮无出站动作（静音或门控跳过）
										</div>
									{:else}
										{#each activeDetail.actions as action, idx (`act-${idx}-${action.type}`)}
											{@const meta = actionMeta(action)}
											<div class="rounded-[var(--radius-panel)] bg-surface-subtle p-3.5 space-y-2">
												<div class="flex flex-wrap items-center justify-between gap-2">
													<div class="flex items-center gap-2">
														<span class="font-mono text-[11px] font-semibold text-ink-muted">
															#{idx + 1}
														</span>
														<Badge tone={meta.tone} size="sm">{meta.label}</Badge>
													</div>
													{#if action.reply_to_msg_id}
														<span class="inline-flex items-center gap-1 rounded-full bg-surface-raised px-2 py-0.5 font-mono text-[11px] text-ink-secondary">
															<Reply class="size-3" />
															{action.reply_to_msg_id}
														</span>
													{/if}
												</div>

												{#if action.type === 'send_text'}
													<div class="flex items-start gap-2 rounded-[var(--radius-control)] bg-surface-raised px-3.5 py-2.5 text-sm text-ink-primary">
														<MessageSquare class="mt-0.5 size-4 shrink-0 text-mint-500" />
														<span class="whitespace-pre-wrap break-words">{action.text ?? ''}</span>
													</div>
												{:else if action.type === 'send_meme'}
													<div class="flex items-center gap-3 rounded-[var(--radius-control)] bg-surface-raised p-2.5">
														{#if action.media?.blob_id}
															<img
																src={api.blobUrl(action.media.blob_id)}
																alt={action.meme_ref ?? 'meme'}
																class="size-14 shrink-0 rounded-xl object-cover"
															/>
														{:else}
															<span class="flex size-12 shrink-0 items-center justify-center rounded-xl bg-tint-amber text-amber-600">
																<ImageIcon class="size-5" />
															</span>
														{/if}
														<div class="min-w-0">
															<div class="truncate font-mono text-xs font-semibold text-ink-primary">
																{action.meme_ref || action.media?.blob_id || 'meme'}
															</div>
															{#if action.text}
																<div class="truncate text-xs text-ink-secondary">{action.text}</div>
															{/if}
														</div>
													</div>
												{:else if action.type === 'send_voice'}
													<div class="flex items-center gap-2.5 rounded-[var(--radius-control)] bg-tint-sky px-3.5 py-2.5 text-xs text-sky-600">
														<Mic class="size-4 shrink-0" />
														<span class="flex-1 truncate font-medium text-ink-primary">
															{action.text || '语音消息'}
														</span>
														{#if action.media?.duration_ms}
															<span class="font-mono text-[11px]">
																{(action.media.duration_ms / 1000).toFixed(1)}s
															</span>
														{/if}
													</div>
												{:else if action.type === 'react'}
													<div class="flex items-center gap-2 text-xs text-ink-primary">
														<Smile class="size-4 text-lilac-600" />
														<span class="rounded-full bg-surface-raised px-2.5 py-1 text-sm">
															{action.emoji ?? '✨'}
														</span>
														{#if action.msg_id}
															<span class="font-mono text-ink-muted">→ {action.msg_id}</span>
														{/if}
													</div>
												{:else if action.type === 'poke'}
													<div class="flex items-center gap-2 text-xs text-ink-primary">
														<Hand class="size-4 text-coral-500" />
														<span class="font-mono font-medium">@{action.user_id ?? 'target'}</span>
													</div>
												{:else}
													<div class="flex items-center gap-2 text-xs text-ink-secondary">
														<Send class="size-3.5" />
														<Sparkles class="size-3.5" />
														<span>{action.text ?? ''}</span>
													</div>
												{/if}
											</div>
										{/each}
									{/if}
								</div>
							{/if}
						{/snippet}
					</Tabs>
				{/if}
			</SurfaceCard>
		</div>
	{/if}
</div>
