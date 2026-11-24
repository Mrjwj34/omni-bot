<script lang="ts">
	import { Spring } from 'svelte/motion';
	import {
		ArrowUpRight,
		Clock,
		MessageSquareText,
		RefreshCw
	} from '@lucide/svelte';
	import {
		api,
		type DashboardResponse,
		type PADVec3,
		type RunStage,
		type RunTraceSummary
	} from '$lib/api';
	import { Badge, Button, PageHeader, SurfaceCard, cn, type BadgeTone } from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	let dashboard = $state<DashboardResponse | null>(null);
	let loading = $state(false);
	let hoveredAxis = $state<'p' | 'a' | 'd' | null>(null);
	let sparkHoverIdx = $state<number | null>(null);

	const padSpring = Spring.of(
		() => ({
			p: realtime.padState.value.p,
			a: realtime.padState.value.a,
			d: realtime.padState.value.d
		}),
		{ stiffness: 0.14, damping: 0.46 }
	);

	const pipelineStages: { key: RunStage; label: string; short: string }[] = [
		{ key: 'gate', label: 'Gate', short: '01' },
		{ key: 'assemble', label: 'Assemble', short: '02' },
		{ key: 'loop', label: 'Agent Loop', short: '03' },
		{ key: 'post_run', label: 'Outbound', short: '04' }
	];

	let activeStageIndex = $derived.by(() => {
		const stage = realtime.activeRun?.stage;
		if (!stage || stage === 'idle') return -1;
		if (stage === 'done') return pipelineStages.length;
		return pipelineStages.findIndex((s) => s.key === stage);
	});

	let isRunActive = $derived(
		Boolean(
			realtime.activeRun &&
				realtime.activeRun.stage !== 'idle' &&
				realtime.activeRun.stage !== 'done' &&
				realtime.activeRun.stage !== 'error'
		)
	);

	let adapters = $derived(
		realtime.adapters.length > 0 ? realtime.adapters : (dashboard?.adapters ?? [])
	);

	let recentTraces = $derived<RunTraceSummary[]>(dashboard?.recent_traces ?? []);

	let padMagnitude = $derived(
		Math.hypot(padSpring.current.p, padSpring.current.a, padSpring.current.d)
	);

	// 3D-projected PAD orb geometry inside 180x180 SVG space
	let orbGeom = $derived.by(() => {
		const p = clamp(padSpring.current.p, -1, 1);
		const a = clamp(padSpring.current.a, -1, 1);
		const d = clamp(padSpring.current.d, -1, 1);
		const cx = 90 + p * 42;
		const cy = 90 - a * 42;
		const radius = 19 + d * 5.5;
		const ringRx = 54 + d * 8;
		const ringRy = 22 + Math.abs(a) * 10;
		return { cx, cy, radius, ringRx, ringRy };
	});

	let orbPalette = $derived.by(() => {
		switch (realtime.moodTone) {
			case 'coral':
				return {
					start: '#ff8e72',
					end: '#fbbf3c',
					glow: 'rgba(255, 115, 84, 0.34)',
					tint: 'bg-tint-peach'
				};
			case 'mint':
				return {
					start: '#34d39e',
					end: '#5bb4ff',
					glow: 'rgba(24, 184, 136, 0.34)',
					tint: 'bg-tint-mint'
				};
			case 'sky':
				return {
					start: '#5bb4ff',
					end: '#ae94ff',
					glow: 'rgba(53, 158, 255, 0.32)',
					tint: 'bg-tint-sky'
				};
			case 'lilac':
				return {
					start: '#ae94ff',
					end: '#ff8e72',
					glow: 'rgba(146, 114, 255, 0.32)',
					tint: 'bg-tint-lilac'
				};
			case 'rose':
				return {
					start: '#fb7188',
					end: '#ff7354',
					glow: 'rgba(239, 77, 105, 0.34)',
					tint: 'bg-tint-rose'
				};
			case 'amber':
				return {
					start: '#fbbf3c',
					end: '#34d39e',
					glow: 'rgba(242, 162, 27, 0.32)',
					tint: 'bg-tint-amber'
				};
			default:
				return {
					start: '#34d39e',
					end: '#18b888',
					glow: 'rgba(24, 184, 136, 0.28)',
					tint: 'bg-surface-subtle'
				};
		}
	});

	interface SparkPoint {
		x: number;
		pY: number;
		aY: number;
		dY: number;
		value: PADVec3;
		timestamp_ms: number;
	}

	const SPARK_W = 360;
	const SPARK_H = 88;
	const SPARK_PAD_X = 8;
	const SPARK_PAD_Y = 10;

	let sparkSeries = $derived.by(() => {
		const raw = realtime.padHistory;
		const pointsSource =
			raw.length >= 2
				? raw
				: [
						{ value: realtime.padState.value, timestamp_ms: Date.now() - 60_000 },
						{ value: realtime.padState.value, timestamp_ms: Date.now() }
					];

		const count = pointsSource.length;
		const innerW = SPARK_W - SPARK_PAD_X * 2;
		const innerH = SPARK_H - SPARK_PAD_Y * 2;
		const toY = (v: number) => SPARK_PAD_Y + ((1 - clamp(v, -1, 1)) / 2) * innerH;

		const pts: SparkPoint[] = pointsSource.map((pt, i) => ({
			x: SPARK_PAD_X + (count === 1 ? innerW : (i / (count - 1)) * innerW),
			pY: toY(pt.value.p),
			aY: toY(pt.value.a),
			dY: toY(pt.value.d),
			value: pt.value,
			timestamp_ms: pt.timestamp_ms
		}));

		const buildPath = (pick: (p: SparkPoint) => number) =>
			pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${pick(p).toFixed(1)}`).join(' ');

		const pPath = buildPath((p) => p.pY);
		const aPath = buildPath((p) => p.aY);
		const dPath = buildPath((p) => p.dY);
		const first = pts[0];
		const last = pts[pts.length - 1];
		const pArea = `${pPath} L${last.x.toFixed(1)},${(SPARK_H / 2).toFixed(1)} L${first.x.toFixed(1)},${(SPARK_H / 2).toFixed(1)} Z`;

		return { pts, pPath, aPath, dPath, pArea, isLiveHistory: raw.length >= 2 };
	});

	let inspectedSpark = $derived(
		sparkHoverIdx !== null ? sparkSeries.pts[sparkHoverIdx] ?? null : null
	);

	async function refreshDashboard(signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const data = await api.getDashboard({ signal });
			dashboard = data;
			if (data.pad) {
				realtime.seedPAD(data.pad);
			}
			if (data.pad_history && data.pad_history.length > 0) {
				realtime.seedPADHistory(data.pad_history);
			}
			if (data.adapters) {
				realtime.seedAdapters(data.adapters);
			}
		} catch {
			// Kernel API may be offline in standalone preview mode; keep graceful standby state.
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void refreshDashboard(ctrl.signal);
		return () => ctrl.abort();
	});

	function clamp(v: number, min: number, max: number): number {
		return Math.max(min, Math.min(max, v));
	}

	function fmtSigned(v: number): string {
		const sign = v > 0 ? '+' : '';
		return `${sign}${v.toFixed(2)}`;
	}

	function bipolarBar(v: number): { left: string; width: string } {
		const clamped = clamp(v, -1, 1);
		const mag = Math.abs(clamped) * 50;
		return clamped >= 0
			? { left: '50%', width: `${mag}%` }
			: { left: `${50 - mag}%`, width: `${mag}%` };
	}

	function formatTime(msOrIso: number | string): string {
		if (!msOrIso) return '--:--:--';
		const d = new Date(msOrIso);
		if (Number.isNaN(d.getTime())) return '--:--:--';
		return d.toLocaleTimeString('zh-CN', { hour12: false });
	}

	function formatUptime(sec?: number): string {
		if (!sec || sec < 0) return '—';
		const h = Math.floor(sec / 3600);
		const m = Math.floor((sec % 3600) / 60);
		if (h > 0) return `${h}h ${m}m`;
		return `${m}m`;
	}

	function traceStatusMeta(status: string): { label: string; tone: BadgeTone } {
		switch (status) {
			case 'completed':
			case 'replied':
				return { label: '已回复', tone: 'mint' };
			case 'silent':
				return { label: '静音', tone: 'amber' };
			case 'gated_out':
			case 'skipped':
				return { label: '跳过', tone: 'neutral' };
			case 'running':
				return { label: '运行中', tone: 'sky' };
			case 'error':
				return { label: '异常', tone: 'rose' };
			default:
				return { label: status, tone: 'neutral' };
		}
	}

	function totalTokens(t: RunTraceSummary): number {
		return (t.tokens_in ?? 0) + (t.tokens_out ?? 0);
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="驾驶舱">
		{#snippet badge()}
			<Badge tone={realtime.moodTone} dot pulse={realtime.connectionStatus === 'connected'}>
				{realtime.moodLabel}
			</Badge>
			{#if dashboard?.uptime_sec}
				<Badge tone="neutral" size="sm">运行 {formatUptime(dashboard.uptime_sec)}</Badge>
			{/if}
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void refreshDashboard()}>
				<RefreshCw class={cn('size-3.5', loading && 'animate-spin')} />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" href="/chat">
				<MessageSquareText class="size-3.5" />
				<span>对话调试</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Top Hero Row: Interactive PAD Mood Hero + Pipeline Stepper -->
	<div class="grid gap-4 lg:grid-cols-12">
		<!-- PAD Mood Hero Card -->
		<SurfaceCard variant="raised" padding="lg" class="flex flex-col gap-4 lg:col-span-6">
			<div class="flex flex-wrap items-center justify-between gap-2">
				<span class="text-sm font-semibold text-ink-primary">PAD 情绪场</span>
				<div class="flex items-center gap-1.5">
					<Badge tone="neutral" size="sm" class="font-mono">|v| {padMagnitude.toFixed(2)}</Badge>
					<Badge tone={realtime.moodTone} size="sm">{realtime.moodLabel}</Badge>
				</div>
			</div>

			<div class="flex flex-col items-center gap-5 sm:flex-row sm:items-center">
				<!-- Flat geometric PAD coordinate plane -->
				<div class="relative flex size-40 shrink-0 items-center justify-center rounded-2xl bg-surface-subtle/70">
					<svg
						viewBox="0 0 180 180"
						class="relative size-40 overflow-visible select-none"
						role="img"
						aria-label={`PAD 情绪坐标 P=${fmtSigned(padSpring.current.p)} A=${fmtSigned(padSpring.current.a)} D=${fmtSigned(padSpring.current.d)}`}
					>
						<!-- Concentric reference rings -->
						<circle
							cx="90"
							cy="90"
							r="64"
							fill="none"
							stroke="#7f948d"
							stroke-opacity="0.18"
							stroke-width="1"
						/>
						<circle
							cx="90"
							cy="90"
							r="36"
							fill="none"
							stroke="#7f948d"
							stroke-opacity="0.14"
							stroke-width="1"
						/>
						<!-- Deadzone boundary (r = 0.18 * 64 ≈ 12px) -->
						<circle
							cx="90"
							cy="90"
							r="12"
							fill="#7f948d"
							fill-opacity="0.06"
							stroke="#7f948d"
							stroke-opacity="0.26"
							stroke-width="1"
							stroke-dasharray="2 2"
						/>
						<!-- D-axis depth ellipse -->
						<ellipse
							cx="90"
							cy="90"
							rx={orbGeom.ringRx}
							ry={orbGeom.ringRy}
							fill="none"
							stroke={hoveredAxis === 'd' ? '#9272ff' : '#7f948d'}
							stroke-opacity={hoveredAxis === 'd' ? '0.6' : '0.18'}
							stroke-width={hoveredAxis === 'd' ? '1.6' : '1'}
						/>
						<!-- P & A crosshairs -->
						<line
							x1="24"
							y1="90"
							x2="156"
							y2="90"
							stroke={hoveredAxis === 'p' ? '#18b888' : '#7f948d'}
							stroke-opacity={hoveredAxis === 'p' ? '0.65' : '0.2'}
							stroke-width={hoveredAxis === 'p' ? '1.6' : '1'}
						/>
						<line
							x1="90"
							y1="24"
							x2="90"
							y2="156"
							stroke={hoveredAxis === 'a' ? '#ff7354' : '#7f948d'}
							stroke-opacity={hoveredAxis === 'a' ? '0.65' : '0.2'}
							stroke-width={hoveredAxis === 'a' ? '1.6' : '1'}
						/>
						<!-- Axis labels -->
						<text x="162" y="93" font-size="9" font-weight="600" fill="#119870">P+</text>
						<text x="8" y="93" font-size="9" font-weight="600" fill="#7f948d">P-</text>
						<text x="84" y="16" font-size="9" font-weight="600" fill="#e85a3c">A+</text>
						<text x="85" y="172" font-size="9" font-weight="600" fill="#7f948d">A-</text>

						<!-- Tether vector from neutral origin -->
						<line
							x1="90"
							y1="90"
							x2={orbGeom.cx}
							y2={orbGeom.cy}
							stroke={orbPalette.start}
							stroke-opacity="0.7"
							stroke-width="1.6"
							stroke-linecap="round"
						/>
						<circle cx="90" cy="90" r="2" fill="#7f948d" fill-opacity="0.5" />

						<!-- Crisp flat coordinate marker -->
						<circle
							cx={orbGeom.cx}
							cy={orbGeom.cy}
							r="10"
							fill={orbPalette.start}
							fill-opacity="0.14"
							stroke={orbPalette.start}
							stroke-opacity="0.55"
							stroke-width="1.2"
						/>
						<circle cx={orbGeom.cx} cy={orbGeom.cy} r="4.5" fill={orbPalette.start} />
					</svg>
				</div>

				<!-- Spring Coordinate Indicators (center-zero bipolar bars) -->
				<div class="w-full min-w-0 flex-1 space-y-3">
					{#each [
						{ key: 'p' as const, label: 'P 愉悦', val: padSpring.current.p, bar: 'bg-mint-500', text: 'text-mint-700' },
						{ key: 'a' as const, label: 'A 唤醒', val: padSpring.current.a, bar: 'bg-coral-500', text: 'text-coral-600' },
						{ key: 'd' as const, label: 'D 掌控', val: padSpring.current.d, bar: 'bg-lilac-500', text: 'text-lilac-600' }
					] as axis (axis.key)}
						{@const geom = bipolarBar(axis.val)}
						<div
							role="presentation"
							onmouseenter={() => (hoveredAxis = axis.key)}
							onmouseleave={() => (hoveredAxis = null)}
							class={cn(
								'space-y-1.5 rounded-[var(--radius-control)] px-2.5 py-1.5 transition-colors',
								hoveredAxis === axis.key ? 'bg-surface-subtle' : 'bg-transparent'
							)}
						>
							<div class="flex items-center justify-between text-xs">
								<span class="font-medium text-ink-secondary">{axis.label}</span>
								<span class={cn('font-mono font-semibold', axis.text)}>{fmtSigned(axis.val)}</span>
							</div>
							<div class="relative h-2 w-full overflow-hidden rounded-full bg-surface-subtle">
								<span
									class="absolute top-0 bottom-0 left-1/2 w-0.5 -translate-x-1/2 bg-ink-muted/25"
								></span>
								<div
									class={cn('absolute top-0 bottom-0 rounded-full transition-all duration-150', axis.bar)}
									style:left={geom.left}
									style:width={geom.width}
								></div>
							</div>
						</div>
					{/each}
				</div>
			</div>

			<!-- 1st-person inner feeling card -->
			<div class={cn('rounded-[var(--radius-panel)] px-4 py-3 transition-colors duration-300', orbPalette.tint)}>
				<div class="flex items-center justify-between gap-2">
					<span class="text-[11px] font-medium text-ink-muted">此刻心境</span>
					{#if realtime.padState.updated_at}
						<span class="font-mono text-[11px] text-ink-muted">
							{formatTime(realtime.padState.updated_at)}
						</span>
					{/if}
				</div>
				<p class="mt-1.5 text-sm leading-relaxed font-medium text-ink-primary">
					{#if realtime.padState.feeling?.trim()}
						“{realtime.padState.feeling.trim()}”
					{:else}
						“现在心境平稳，正轻松地听大家聊天。”
					{/if}
				</p>
			</div>

			<!-- Multi-axis PAD Waveform Sparkline -->
			<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-3.5 py-3">
				<div class="mb-2 flex flex-wrap items-center justify-between gap-2">
					<div class="flex items-center gap-2 text-xs font-medium text-ink-secondary">
						<span>PAD 波形轨迹</span>
						<span class="font-mono text-[11px] text-ink-muted">
							{sparkSeries.isLiveHistory ? `${realtime.padHistory.length} 点` : '实时待采样'}
						</span>
					</div>
					<div class="flex items-center gap-3 font-mono text-[11px]">
						<span class="inline-flex items-center gap-1 text-mint-700">
							<span class="size-2 rounded-full bg-mint-500"></span>
							P {fmtSigned((inspectedSpark?.value ?? realtime.padState.value).p)}
						</span>
						<span class="inline-flex items-center gap-1 text-coral-600">
							<span class="size-2 rounded-full bg-coral-500"></span>
							A {fmtSigned((inspectedSpark?.value ?? realtime.padState.value).a)}
						</span>
						<span class="inline-flex items-center gap-1 text-lilac-600">
							<span class="size-2 rounded-full bg-lilac-500"></span>
							D {fmtSigned((inspectedSpark?.value ?? realtime.padState.value).d)}
						</span>
					</div>
				</div>

				<svg
					viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
					class="h-20 w-full overflow-visible"
					role="img"
					aria-label="PAD 多轴实时波形图"
					onmouseleave={() => (sparkHoverIdx = null)}
				>
					<defs>
						<linearGradient id="sparkMintArea" x1="0" y1="0" x2="0" y2="1">
							<stop offset="0%" stop-color="#18b888" stop-opacity="0.18" />
							<stop offset="100%" stop-color="#18b888" stop-opacity="0" />
						</linearGradient>
					</defs>

					<!-- Zero baseline -->
					<line
						x1={SPARK_PAD_X}
						y1={SPARK_H / 2}
						x2={SPARK_W - SPARK_PAD_X}
						y2={SPARK_H / 2}
						stroke="#7f948d"
						stroke-opacity="0.25"
						stroke-dasharray="3 3"
						stroke-width="1"
					/>

					<path d={sparkSeries.pArea} fill="url(#sparkMintArea)" />
					<path
						d={sparkSeries.dPath}
						fill="none"
						stroke="#9272ff"
						stroke-width="1.8"
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-opacity="0.85"
					/>
					<path
						d={sparkSeries.aPath}
						fill="none"
						stroke="#ff7354"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-opacity="0.9"
					/>
					<path
						d={sparkSeries.pPath}
						fill="none"
						stroke="#18b888"
						stroke-width="2.2"
						stroke-linecap="round"
						stroke-linejoin="round"
					/>

					{#if inspectedSpark}
						<line
							x1={inspectedSpark.x}
							y1={SPARK_PAD_Y}
							x2={inspectedSpark.x}
							y2={SPARK_H - SPARK_PAD_Y}
							stroke="#1a2724"
							stroke-opacity="0.16"
							stroke-width="1"
						/>
						<circle cx={inspectedSpark.x} cy={inspectedSpark.pY} r="3.5" fill="#18b888" />
						<circle cx={inspectedSpark.x} cy={inspectedSpark.aY} r="3.5" fill="#ff7354" />
						<circle cx={inspectedSpark.x} cy={inspectedSpark.dY} r="3.5" fill="#9272ff" />
					{:else if sparkSeries.pts.length > 0}
						{@const last = sparkSeries.pts[sparkSeries.pts.length - 1]}
						<circle cx={last.x} cy={last.pY} r="3.5" fill="#18b888" />
						<circle cx={last.x} cy={last.aY} r="3" fill="#ff7354" />
						<circle cx={last.x} cy={last.dY} r="3" fill="#9272ff" />
					{/if}

					{#each sparkSeries.pts as pt, idx (`spark-${idx}`)}
						<rect
							x={pt.x - Math.max(8, SPARK_W / Math.max(sparkSeries.pts.length, 2) / 2)}
							y="0"
							width={Math.max(16, SPARK_W / Math.max(sparkSeries.pts.length, 2))}
							height={SPARK_H}
							fill="transparent"
							role="presentation"
							onmouseenter={() => (sparkHoverIdx = idx)}
						/>
					{/each}
				</svg>
			</div>
		</SurfaceCard>

		<!-- Right Column: Active Run Pipeline Stepper + Metric Tiles -->
		<div class="flex flex-col gap-4 lg:col-span-6">
			<SurfaceCard variant="raised" padding="lg" class="flex flex-col justify-between gap-4">
				<div class="flex flex-wrap items-center justify-between gap-2">
					<span class="text-sm font-semibold text-ink-primary">Run 实时管线</span>
					{#if realtime.activeRun}
						<Badge tone={isRunActive ? 'sky' : 'mint'} size="sm" dot pulse={isRunActive}>
							{realtime.activeRun.run_id}
						</Badge>
					{:else}
						<Badge tone="neutral" size="sm">空闲待命</Badge>
					{/if}
				</div>

				<div class="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
					{#each pipelineStages as stage, idx (stage.key)}
						{@const isCurrent = activeStageIndex === idx}
						{@const isDone = activeStageIndex > idx}
						<div
							class={cn(
								'relative flex flex-col gap-2 rounded-[var(--radius-panel)] px-3 py-3 transition-all duration-200',
								isCurrent
									? 'bg-tint-mint text-mint-700 shadow-[var(--shadow-xs)]'
									: isDone
										? 'bg-tint-sky/70 text-sky-600'
										: 'bg-surface-subtle text-ink-muted'
							)}
						>
							<div class="flex items-center justify-between">
								<span class="font-mono text-[11px] font-semibold">{stage.short}</span>
								<span class="relative inline-flex size-2.5">
									{#if isCurrent}
										<span
											class="absolute inline-flex size-full animate-ping rounded-full bg-mint-500 opacity-70"
										></span>
									{/if}
									<span
										class={cn(
											'relative inline-flex size-2.5 rounded-full',
											isCurrent
												? 'bg-mint-500'
												: isDone
													? 'bg-sky-500'
													: 'bg-ink-muted/30'
										)}
									></span>
								</span>
							</div>
							<div class="whitespace-nowrap text-[11px] font-semibold text-ink-primary">{stage.label}</div>
						</div>
					{/each}
				</div>

				<div
					class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-panel)] bg-surface-subtle px-3.5 py-2.5 text-xs"
				>
					<span class="truncate text-ink-secondary">
						{#if realtime.activeRun}
							房间
							<span class="font-mono font-semibold text-ink-primary">
								{realtime.activeRun.room_id || 'default'}
							</span>
							· {realtime.activeRun.steps.length} 步
						{:else}
							Single-Flight 串行总线就绪
						{/if}
					</span>
					<a
						href={realtime.activeRun
							? `/traces?run=${encodeURIComponent(realtime.activeRun.run_id)}`
							: '/traces'}
						class="inline-flex shrink-0 items-center gap-1 font-semibold text-mint-600 hover:text-mint-700"
					>
						<span>查看轨迹</span>
						<ArrowUpRight class="size-3.5" />
					</a>
				</div>
			</SurfaceCard>

			<!-- Compact Tonal Metric Tiles -->
			<div class="grid flex-1 grid-cols-2 gap-3.5">
				<a href="/chat" class="block focus-visible:outline-none">
					<SurfaceCard variant="tint-peach" padding="md" interactive class="flex h-full flex-col justify-between">
						<span class="text-xs font-medium text-ink-secondary">时间线消息</span>
						<div class="mt-3 flex items-baseline justify-between gap-2">
							<span class="font-mono text-2xl font-bold text-ink-primary">
								{dashboard?.metrics.messages_total !== undefined
									? dashboard.metrics.messages_total.toLocaleString()
									: (dashboard?.metrics.replies_today ?? '—')}
							</span>
							<span class="font-mono text-[11px] text-coral-600">
								今日回复 {dashboard?.metrics.replies_today ?? 0}
							</span>
						</div>
					</SurfaceCard>
				</a>

				<a href="/memory" class="block focus-visible:outline-none">
					<SurfaceCard variant="tint-lilac" padding="md" interactive class="flex h-full flex-col justify-between">
						<span class="text-xs font-medium text-ink-secondary">活跃记忆</span>
						<div class="mt-3 flex items-baseline justify-between gap-2">
							<span class="font-mono text-2xl font-bold text-ink-primary">
								{dashboard?.metrics.memories_total ?? '—'}
							</span>
							<span class="text-[11px] font-medium text-lilac-600">图谱 →</span>
						</div>
					</SurfaceCard>
				</a>

				<a href="/memes" class="block focus-visible:outline-none">
					<SurfaceCard variant="tint-amber" padding="md" interactive class="flex h-full flex-col justify-between">
						<span class="text-xs font-medium text-ink-secondary">表情金库</span>
						<div class="mt-3 flex items-baseline justify-between gap-2">
							<span class="font-mono text-2xl font-bold text-ink-primary">
								{dashboard?.metrics.memes_total ?? '—'}
							</span>
							<span class="text-[11px] font-medium text-amber-600">手牌 →</span>
						</div>
					</SurfaceCard>
				</a>

				<a href="/traces" class="block focus-visible:outline-none">
					<SurfaceCard variant="tint-mint" padding="md" interactive class="flex h-full flex-col justify-between">
						<span class="text-xs font-medium text-ink-secondary">近期 Run</span>
						<div class="mt-3 flex items-baseline justify-between gap-2">
							<span class="font-mono text-2xl font-bold text-ink-primary">
								{dashboard?.metrics.runs_today ?? '—'}
							</span>
							{#if dashboard?.metrics.cache_hit_ratio !== undefined}
								<span class="font-mono text-[11px] text-mint-700">
									缓存 {Math.round(dashboard.metrics.cache_hit_ratio * 100)}%
								</span>
							{/if}
						</div>
					</SurfaceCard>
				</a>
			</div>
		</div>
	</div>

	<!-- Bottom Split: Latest Run Traces Preview + Adapters & Live Pulse -->
	<div class="grid gap-4 lg:grid-cols-12">
		<SurfaceCard variant="raised" padding="md" class="lg:col-span-7">
			<div class="mb-3 flex items-center justify-between gap-2">
				<div class="flex items-center gap-2">
					<span class="text-sm font-semibold text-ink-primary">最新 Run 轨迹</span>
					<Badge tone="neutral" size="sm">{recentTraces.length}</Badge>
				</div>
				<a
					href="/traces"
					class="inline-flex items-center gap-1 text-xs font-semibold text-mint-600 hover:text-mint-700"
				>
					<span>全部</span>
					<ArrowUpRight class="size-3.5" />
				</a>
			</div>

			{#if recentTraces.length === 0 && !realtime.activeRun}
				<div
					class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted"
				>
					暂无近期 Run 记录
				</div>
			{:else}
				<ul class="space-y-2">
					{#if realtime.activeRun && !recentTraces.some((t) => t.run_id === realtime.activeRun?.run_id)}
						<li>
							<a
								href={`/traces?run=${encodeURIComponent(realtime.activeRun.run_id)}`}
								class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-panel)] bg-tint-sky px-3.5 py-2.5 text-xs transition-transform duration-150 hover:-translate-y-0.5"
							>
								<div class="flex min-w-0 items-center gap-2">
									<Badge tone="sky" size="sm" dot pulse>运行中</Badge>
									<span class="truncate font-mono font-semibold text-ink-primary">
										{realtime.activeRun.run_id}
									</span>
									<span class="truncate font-mono text-ink-secondary">
										{realtime.activeRun.room_id || 'default'}
									</span>
								</div>
								<span class="shrink-0 font-mono text-[11px] text-sky-600">
									{realtime.activeRun.stage} · {realtime.activeRun.steps.length} 步
								</span>
							</a>
						</li>
					{/if}

					{#each recentTraces.slice(0, 5) as trace (trace.run_id)}
						{@const st = traceStatusMeta(trace.status)}
						{@const toks = totalTokens(trace)}
						<li>
							<a
								href={`/traces?run=${encodeURIComponent(trace.run_id)}`}
								class="group flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-panel)] bg-surface-subtle px-3.5 py-2.5 text-xs transition-all duration-150 hover:bg-surface-muted/80"
							>
								<div class="flex min-w-0 flex-1 items-center gap-2">
									<Badge tone={st.tone} size="sm">{st.label}</Badge>
									<span class="truncate font-mono font-semibold text-ink-primary">
										{trace.room_id || trace.run_id}
									</span>
									<span class="hidden truncate rounded-full bg-surface-raised px-2 py-0.5 text-[11px] text-ink-secondary sm:inline">
										{trace.source}
									</span>
								</div>
								<div class="flex shrink-0 items-center gap-2.5 font-mono text-[11px] text-ink-muted">
									{#if toks > 0}
										<span>{toks} tok</span>
									{/if}
									<span>{Math.round(trace.duration_ms)}ms</span>
									<span class="inline-flex items-center gap-1">
										<Clock class="size-3" />
										{formatTime(trace.started_at)}
									</span>
									<ArrowUpRight
										class="size-3.5 text-ink-muted transition-transform group-hover:translate-x-0.5 group-hover:-translate-y-0.5 group-hover:text-mint-600"
									/>
								</div>
							</a>
						</li>
					{/each}
				</ul>
			{/if}
		</SurfaceCard>

		<!-- Connected Adapters & Live Event Pulse -->
		<SurfaceCard variant="raised" padding="md" class="flex flex-col gap-4 lg:col-span-5">
			<div>
				<div class="mb-2.5 flex items-center justify-between gap-2">
					<span class="text-sm font-semibold text-ink-primary">已连通道</span>
					<Badge tone={adapters.length > 0 ? 'mint' : 'neutral'} size="sm">
						{adapters.length} 在线
					</Badge>
				</div>

				{#if adapters.length === 0}
					<div
						class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-4 text-center text-xs text-ink-muted"
					>
						等待 NapCat / 调试前端接入
					</div>
				{:else}
					<ul class="space-y-2">
						{#each adapters as adapter (adapter.channel_id)}
							<li class="rounded-[var(--radius-panel)] bg-surface-subtle p-3">
								<div class="flex items-center justify-between gap-2">
									<span class="truncate text-xs font-semibold text-ink-primary">
										{adapter.channel_id}
									</span>
									<Badge tone="sky" size="sm">{adapter.kind}</Badge>
								</div>
								{#if adapter.capabilities && adapter.capabilities.length > 0}
									<div class="mt-2 flex flex-wrap gap-1">
										{#each adapter.capabilities as cap (cap)}
											<span
												class="rounded-full bg-surface-raised px-2 py-0.5 text-[10px] font-medium text-ink-secondary"
											>
												{cap}
											</span>
										{/each}
									</div>
								{/if}
							</li>
						{/each}
					</ul>
				{/if}
			</div>

			<div>
				<div class="mb-2 flex items-center justify-between gap-2">
					<span class="text-xs font-semibold text-ink-secondary">实时事件脉搏</span>
					<Badge tone="neutral" size="sm">{realtime.recentEvents.length}</Badge>
				</div>

				{#if realtime.recentEvents.length === 0}
					<div
						class="rounded-[var(--radius-panel)] bg-surface-subtle px-3 py-4 text-center text-xs text-ink-muted"
					>
						等待 WS 推送
					</div>
				{:else}
					<ul class="space-y-1.5">
						{#each realtime.recentEvents.slice(0, 4) as ev, i (`${ev.topic}-${ev.timestamp_ms}-${i}`)}
							<li
								class="flex items-center justify-between gap-2 rounded-[var(--radius-control)] bg-surface-subtle px-3 py-1.5 text-xs"
							>
								<div class="flex min-w-0 items-center gap-2">
									<Badge tone="mint" size="sm">{ev.topic}</Badge>
									{#if ev.room_id}
										<span class="truncate font-mono text-[11px] text-ink-secondary">
											{ev.room_id}
										</span>
									{/if}
								</div>
								<span class="shrink-0 font-mono text-[11px] text-ink-muted">
									{formatTime(ev.timestamp_ms)}
								</span>
							</li>
						{/each}
					</ul>
				{/if}
			</div>
		</SurfaceCard>
	</div>
</div>
