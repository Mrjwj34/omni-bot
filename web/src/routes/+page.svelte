<script lang="ts">
	import { Spring } from 'svelte/motion';
	import {
		Activity,
		Brain,
		MessageSquareText,
		Radio,
		RefreshCw,
		SmilePlus,
		Sparkles,
		Zap
	} from '@lucide/svelte';
	import { api, type DashboardResponse, type RunStage } from '$lib/api';
	import { Badge, Button, PageHeader, SurfaceCard, cn } from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	let dashboard = $state<DashboardResponse | null>(null);
	let loading = $state(false);

	const padSpring = Spring.of(
		() => ({
			p: realtime.padState.value.p,
			a: realtime.padState.value.a,
			d: realtime.padState.value.d
		}),
		{ stiffness: 0.14, damping: 0.48 }
	);

	const pipelineStages: { key: RunStage; label: string }[] = [
		{ key: 'gate', label: 'OnGate' },
		{ key: 'assemble', label: 'OnAssemble' },
		{ key: 'loop', label: 'Agent Loop' },
		{ key: 'post_run', label: 'OnPostRun' }
	];

	let activeStageIndex = $derived.by(() => {
		const stage = realtime.activeRun?.stage;
		if (!stage || stage === 'idle') return -1;
		if (stage === 'done') return pipelineStages.length;
		return pipelineStages.findIndex((s) => s.key === stage);
	});

	let adapters = $derived(
		realtime.adapters.length > 0 ? realtime.adapters : (dashboard?.adapters ?? [])
	);

	async function refreshDashboard(signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const data = await api.getDashboard({ signal });
			dashboard = data;
			if (data.pad) {
				realtime.seedPAD(data.pad);
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

	function axisPct(v: number): number {
		return Math.max(0, Math.min(100, ((v + 1) / 2) * 100));
	}

	function fmtSigned(v: number): string {
		const sign = v > 0 ? '+' : '';
		return `${sign}${v.toFixed(2)}`;
	}

	function formatTime(ms: number): string {
		if (!ms) return '--:--:--';
		return new Date(ms).toLocaleTimeString('zh-CN', { hour12: false });
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="驾驶舱">
		{#snippet badge()}
			<Badge tone={realtime.moodTone} dot pulse={realtime.connectionStatus === 'connected'}>
				{realtime.moodLabel}
			</Badge>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void refreshDashboard()}>
				<RefreshCw class="size-3.5" />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" href="/chat">
				<MessageSquareText class="size-3.5" />
				<span>对话调试</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Top Hero Row: PAD Mood Orb + Active Pipeline Stepper -->
	<div class="grid gap-4 lg:grid-cols-12">
		<SurfaceCard variant="raised" padding="lg" class="lg:col-span-5">
			<div class="flex items-center justify-between gap-2">
				<div class="flex items-center gap-2">
					<span class="flex size-8 items-center justify-center rounded-xl bg-tint-mint text-mint-600">
						<Sparkles class="size-4" />
					</span>
					<span class="text-sm font-semibold text-ink-primary">实时情绪 PAD</span>
				</div>
				<Badge tone={realtime.moodTone} size="sm">{realtime.moodLabel}</Badge>
			</div>

			<div class="mt-5 flex items-center gap-5">
				<div class="relative flex size-24 shrink-0 items-center justify-center">
					<div
						class="absolute inset-0 rounded-full bg-gradient-to-tr from-mint-400/35 via-coral-400/25 to-lilac-400/35 blur-xl transition-transform duration-300"
						style:transform={`scale(${0.95 + Math.abs(padSpring.current.a) * 0.25})`}
					></div>
					<div
						class="relative flex size-20 items-center justify-center rounded-full bg-gradient-to-br from-mint-400 via-mint-500 to-sky-400 text-white shadow-[0_12px_28px_-10px_rgba(24,184,136,0.65)]"
						style:transform={`translate(${padSpring.current.p * 6}px, ${-padSpring.current.a * 6}px)`}
					>
						<div class="text-center font-mono leading-tight">
							<div class="text-xs font-bold">{fmtSigned(padSpring.current.p)}</div>
							<div class="text-[10px] opacity-85">P 轴</div>
						</div>
					</div>
				</div>

				<div class="min-w-0 flex-1 space-y-3">
					{#each [
						{ label: 'P 愉悦', val: padSpring.current.p, bar: 'bg-mint-500' },
						{ label: 'A 唤醒', val: padSpring.current.a, bar: 'bg-coral-500' },
						{ label: 'D 掌控', val: padSpring.current.d, bar: 'bg-lilac-500' }
					] as axis (axis.label)}
						<div class="space-y-1">
							<div class="flex items-center justify-between text-xs">
								<span class="font-medium text-ink-secondary">{axis.label}</span>
								<span class="font-mono font-semibold text-ink-primary">{fmtSigned(axis.val)}</span>
							</div>
							<div class="h-2 w-full overflow-hidden rounded-full bg-surface-subtle">
								<div
									class={cn('h-full rounded-full transition-[width] duration-150', axis.bar)}
									style:width={`${axisPct(axis.val)}%`}
								></div>
							</div>
						</div>
					{/each}
				</div>
			</div>
		</SurfaceCard>

		<SurfaceCard variant="raised" padding="lg" class="flex flex-col justify-between lg:col-span-7">
			<div class="flex flex-wrap items-center justify-between gap-2">
				<div class="flex items-center gap-2">
					<span class="flex size-8 items-center justify-center rounded-xl bg-tint-sky text-sky-600">
						<Zap class="size-4" />
					</span>
					<span class="text-sm font-semibold text-ink-primary">执行管线</span>
				</div>
				{#if realtime.activeRun}
					<Badge tone="sky" size="sm" dot pulse={realtime.activeRun.stage !== 'done'}>
						{realtime.activeRun.run_id}
					</Badge>
				{:else}
					<Badge tone="neutral" size="sm">空闲待命</Badge>
				{/if}
			</div>

			<div class="my-5 grid grid-cols-2 gap-2.5 sm:grid-cols-4">
				{#each pipelineStages as stage, idx (stage.key)}
					{@const isCurrent = activeStageIndex === idx}
					{@const isDone = activeStageIndex > idx}
					<div
						class={cn(
							'flex flex-col gap-1.5 rounded-[var(--radius-panel)] p-3 transition-all duration-200',
							isCurrent
								? 'bg-tint-mint text-mint-700 shadow-[var(--shadow-xs)]'
								: isDone
									? 'bg-tint-sky/60 text-sky-600'
									: 'bg-surface-subtle text-ink-muted'
						)}
					>
						<div class="flex items-center justify-between">
							<span class="font-mono text-[11px] font-semibold">0{idx + 1}</span>
							<span
								class={cn(
									'size-2 rounded-full',
									isCurrent
										? 'animate-pulse bg-mint-500'
										: isDone
											? 'bg-sky-500'
											: 'bg-ink-muted/35'
								)}
							></span>
						</div>
						<div class="text-xs font-semibold text-ink-primary">{stage.label}</div>
					</div>
				{/each}
			</div>

			<div class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-panel)] bg-surface-subtle px-3.5 py-2.5 text-xs">
				<span class="text-ink-secondary">
					{#if realtime.activeRun}
						房间 <span class="font-mono font-semibold text-ink-primary">{realtime.activeRun.room_id || 'default'}</span> · {realtime.activeRun.steps.length} 步
					{:else}
						等待新消息触发 Single-Flight Run
					{/if}
				</span>
				<a href="/traces" class="font-semibold text-mint-600 hover:text-mint-700">查看轨迹 →</a>
			</div>
		</SurfaceCard>
	</div>

	<!-- Quick Metrics Row -->
	<div class="grid grid-cols-2 gap-3.5 lg:grid-cols-4">
		<SurfaceCard variant="tint-mint" padding="md" interactive>
			<div class="flex items-center justify-between">
				<span class="text-xs font-medium text-ink-secondary">今日 Run</span>
				<Activity class="size-4 text-mint-600" />
			</div>
			<div class="mt-2 font-mono text-2xl font-bold text-ink-primary">
				{dashboard?.metrics.runs_today ?? '—'}
			</div>
		</SurfaceCard>

		<SurfaceCard variant="tint-sky" padding="md" interactive>
			<div class="flex items-center justify-between">
				<span class="text-xs font-medium text-ink-secondary">已连通道</span>
				<Radio class="size-4 text-sky-600" />
			</div>
			<div class="mt-2 font-mono text-2xl font-bold text-ink-primary">{adapters.length}</div>
		</SurfaceCard>

		<SurfaceCard variant="tint-lilac" padding="md" interactive>
			<div class="flex items-center justify-between">
				<span class="text-xs font-medium text-ink-secondary">记忆条目</span>
				<Brain class="size-4 text-lilac-600" />
			</div>
			<div class="mt-2 font-mono text-2xl font-bold text-ink-primary">
				{dashboard?.metrics.memories_total ?? '—'}
			</div>
		</SurfaceCard>

		<SurfaceCard variant="tint-amber" padding="md" interactive>
			<div class="flex items-center justify-between">
				<span class="text-xs font-medium text-ink-secondary">表情金库</span>
				<SmilePlus class="size-4 text-amber-600" />
			</div>
			<div class="mt-2 font-mono text-2xl font-bold text-ink-primary">
				{dashboard?.metrics.memes_total ?? '—'}
			</div>
		</SurfaceCard>
	</div>

	<!-- Bottom Split: Live Event Stream + Connected Adapters -->
	<div class="grid gap-4 lg:grid-cols-12">
		<SurfaceCard variant="raised" padding="md" class="lg:col-span-7">
			<div class="mb-3 flex items-center justify-between">
				<span class="text-sm font-semibold text-ink-primary">实时事件流</span>
				<Badge tone="neutral" size="sm">{realtime.recentEvents.length}</Badge>
			</div>

			{#if realtime.recentEvents.length === 0}
				<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
					暂无推送事件
				</div>
			{:else}
				<ul class="space-y-1.5">
					{#each realtime.recentEvents.slice(0, 6) as ev, i (`${ev.topic}-${ev.timestamp_ms}-${i}`)}
						<li
							class="flex items-center justify-between gap-3 rounded-[var(--radius-control)] bg-surface-subtle px-3 py-2 text-xs"
						>
							<div class="flex min-w-0 items-center gap-2">
								<Badge tone="mint" size="sm">{ev.topic}</Badge>
								{#if ev.room_id}
									<span class="truncate font-mono text-ink-secondary">{ev.room_id}</span>
								{/if}
							</div>
							<span class="shrink-0 font-mono text-[11px] text-ink-muted">
								{formatTime(ev.timestamp_ms)}
							</span>
						</li>
					{/each}
				</ul>
			{/if}
		</SurfaceCard>

		<SurfaceCard variant="raised" padding="md" class="lg:col-span-5">
			<div class="mb-3 flex items-center justify-between">
				<span class="text-sm font-semibold text-ink-primary">前端适配器</span>
				<Badge tone={adapters.length > 0 ? 'mint' : 'neutral'} size="sm">
					{adapters.length} 在线
				</Badge>
			</div>

			{#if adapters.length === 0}
				<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
					等待 NapCat 或调试前端接入
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
										<span class="rounded-full bg-surface-raised px-2 py-0.5 text-[10px] font-medium text-ink-secondary">
											{cap}
										</span>
									{/each}
								</div>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</SurfaceCard>
	</div>
</div>
