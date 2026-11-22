<script lang="ts">
	import {
		Archive,
		ArrowRight,
		Brain,
		Calendar,
		CheckCircle2,
		Flame,
		GitBranch,
		Hash,
		RefreshCw,
		RotateCcw,
		Search,
		Sparkles,
		Tag,
		X
	} from '@lucide/svelte';
	import { api, type DreamingStatus, type MemoryItem } from '$lib/api';
	import {
		Badge,
		Button,
		EmptyState,
		Input,
		PageHeader,
		SurfaceCard,
		cn,
		type BadgeTone
	} from '$lib/components/ui';

	type KindFilter = 'all' | 'habit' | 'semantic' | 'episodic';
	type ArchiveView = 'active' | 'archived';

	type ExtendedMemory = Omit<MemoryItem, 'kind'> & {
		kind: string;
		archived?: boolean;
		importance?: number;
		t_event_ms?: number;
	};

	interface EntityAliasRecord {
		alias: string;
		canonical_entity: string;
		sender_id?: string;
	}

	interface ExtendedMemoryListResponse {
		items: ExtendedMemory[];
		dreaming?: DreamingStatus;
		aliases?: EntityAliasRecord[];
		alias_map?: Record<string, string[]>;
	}

	interface AliasGroup {
		canonical: string;
		aliases: string[];
		memoryCount: number;
	}

	let query = $state('');
	let activeQuery = $state('');
	let kindFilter = $state<KindFilter>('all');
	let archiveView = $state<ArchiveView>('active');
	let selectedEntity = $state<string | null>(null);
	let highlightedId = $state<string | null>(null);

	let items = $state.raw<ExtendedMemory[]>([]);
	let dreaming = $state<DreamingStatus | null>(null);
	let rawAliases = $state.raw<EntityAliasRecord[]>([]);
	let rawAliasMap = $state.raw<Record<string, string[]>>({});
	let loading = $state(false);
	let searchElapsedMs = $state<number | null>(null);

	let dreamingLoading = $state(false);
	let dreamToast = $state<{ tone: 'mint' | 'rose'; text: string } | null>(null);

	const kindPills: { key: KindFilter; label: string; tone: BadgeTone }[] = [
		{ key: 'all', label: '全部', tone: 'neutral' },
		{ key: 'habit', label: '习惯 Habit', tone: 'coral' },
		{ key: 'semantic', label: '语义 Semantic', tone: 'sky' },
		{ key: 'episodic', label: '情景 Episodic', tone: 'lilac' }
	];

	function normalizeKind(kind: string): Exclude<KindFilter, 'all'> {
		switch (kind.toLowerCase()) {
			case 'habit':
			case 'preference':
				return 'habit';
			case 'semantic':
			case 'fact':
			case 'relation':
				return 'semantic';
			case 'episodic':
			case 'episode':
			default:
				return 'episodic';
		}
	}

	function kindMeta(kind: string): { label: string; tone: BadgeTone } {
		switch (normalizeKind(kind)) {
			case 'habit':
				return { label: '习惯', tone: 'coral' };
			case 'semantic':
				return { label: '语义', tone: 'sky' };
			case 'episodic':
				return { label: '情景', tone: 'lilac' };
		}
	}

	function isArchivedItem(item: ExtendedMemory): boolean {
		return Boolean(item.archived);
	}

	let supersededCountByTarget = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const item of items) {
			if (item.superseded_by) {
				counts.set(item.superseded_by, (counts.get(item.superseded_by) ?? 0) + 1);
			}
		}
		return counts;
	});

	let activeCount = $derived(items.filter((m) => !isArchivedItem(m)).length);
	let archivedCount = $derived(items.filter((m) => isArchivedItem(m)).length);

	let filteredItems = $derived.by(() => {
		return items.filter((item) => {
			const archived = isArchivedItem(item);
			if (archiveView === 'active' && archived) return false;
			if (archiveView === 'archived' && !archived) return false;
			if (kindFilter !== 'all' && normalizeKind(item.kind) !== kindFilter) return false;
			if (selectedEntity && !(item.entities ?? []).includes(selectedEntity)) return false;
			return true;
		});
	});

	let aliasGroups = $derived.by<AliasGroup[]>(() => {
		const map = new Map<string, Set<string>>();
		const counts = new Map<string, number>();

		for (const item of items) {
			for (const ent of item.entities ?? []) {
				const key = ent.trim();
				if (!key) continue;
				if (!map.has(key)) map.set(key, new Set());
				counts.set(key, (counts.get(key) ?? 0) + 1);
			}
		}

		for (const [canonical, aliases] of Object.entries(rawAliasMap)) {
			const key = canonical.trim();
			if (!key) continue;
			if (!map.has(key)) map.set(key, new Set());
			const set = map.get(key)!;
			for (const a of aliases ?? []) {
				if (a.trim() && a.trim() !== key) set.add(a.trim());
			}
		}

		for (const rec of rawAliases) {
			const key = rec.canonical_entity?.trim();
			const alias = rec.alias?.trim();
			if (!key) continue;
			if (!map.has(key)) map.set(key, new Set());
			if (alias && alias !== key) map.get(key)!.add(alias);
		}

		return Array.from(map.entries())
			.map(([canonical, aliasSet]) => ({
				canonical,
				aliases: Array.from(aliasSet).sort((a, b) => a.localeCompare(b)),
				memoryCount: counts.get(canonical) ?? 0
			}))
			.sort((a, b) => b.memoryCount - a.memoryCount || a.canonical.localeCompare(b.canonical));
	});

	async function loadMemories(signal?: AbortSignal): Promise<void> {
		loading = true;
		searchElapsedMs = null;
		activeQuery = '';
		try {
			const res = (await api.listMemories({ limit: 100 }, { signal })) as ExtendedMemoryListResponse;
			items = res.items ?? [];
			if (res.dreaming) dreaming = res.dreaming;
			rawAliases = res.aliases ?? [];
			rawAliasMap = res.alias_map ?? {};
		} catch {
			// Standalone offline fallback keeps UI clean.
		} finally {
			loading = false;
		}
	}

	async function runSearch(): Promise<void> {
		const q = query.trim();
		if (!q) {
			await loadMemories();
			return;
		}
		loading = true;
		activeQuery = q;
		try {
			const res = await api.searchMemories(q, { limit: 60 });
			items = (res.items ?? []) as ExtendedMemory[];
			searchElapsedMs = res.elapsed_ms;
		} catch {
			// Fallback local client filter if offline
			const lower = q.toLowerCase();
			items = items.filter(
				(m) =>
					m.content.toLowerCase().includes(lower) ||
					(m.entities ?? []).some((e) => e.toLowerCase().includes(lower))
			);
			searchElapsedMs = 0;
		} finally {
			loading = false;
		}
	}

	async function triggerDreaming(): Promise<void> {
		dreamingLoading = true;
		dreamToast = null;
		try {
			const res = await api.triggerDreaming();
			dreamToast = {
				tone: 'mint',
				text: res.accepted ? 'Dreaming 后台巩固已触发' : 'Dreaming 已在运行中'
			};
			if (dreaming) {
				dreaming = { ...dreaming, running: true };
			}
			void loadMemories();
		} catch (err) {
			dreamToast = {
				tone: 'rose',
				text: err instanceof Error ? err.message : '触发 Dreaming 失败'
			};
		} finally {
			dreamingLoading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void loadMemories(ctrl.signal);
		return () => ctrl.abort();
	});

	$effect(() => {
		if (!dreamToast) return;
		const timer = setTimeout(() => {
			dreamToast = null;
		}, 4800);
		return () => clearTimeout(timer);
	});

	$effect(() => {
		if (!highlightedId) return;
		const timer = setTimeout(() => {
			highlightedId = null;
		}, 2600);
		return () => clearTimeout(timer);
	});

	function jumpToMemory(targetId: string): void {
		const target = items.find((m) => m.id === targetId);
		if (target) {
			archiveView = isArchivedItem(target) ? 'archived' : 'active';
			kindFilter = 'all';
			selectedEntity = null;
		}
		highlightedId = targetId;
		if (typeof document !== 'undefined') {
			requestAnimationFrame(() => {
				document.getElementById(`mem-${targetId}`)?.scrollIntoView({
					behavior: 'smooth',
					block: 'center'
				});
			});
		}
	}

	function formatEventDate(item: ExtendedMemory): string {
		if (item.t_event) {
			if (/^\d{4}-\d{2}-\d{2}/.test(item.t_event)) return item.t_event.slice(0, 10);
			const parsed = new Date(item.t_event);
			if (!Number.isNaN(parsed.getTime())) return parsed.toISOString().slice(0, 10);
		}
		const ts = item.t_event_ms || (item.created_at ? Date.parse(item.created_at) : 0);
		if (ts && !Number.isNaN(ts)) {
			return new Date(ts).toISOString().slice(0, 10);
		}
		return '----/--/--';
	}

	function formatImportance(item: ExtendedMemory): string {
		if (typeof item.importance === 'number' && item.importance > 0) {
			return `Lv.${item.importance}`;
		}
		const scaled = Math.max(1, Math.min(5, Math.round((item.strength ?? 0.5) * 5)));
		return `Lv.${scaled}`;
	}

	function fmtClock(iso?: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return iso;
		return d.toLocaleTimeString('zh-CN', { hour12: false });
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="记忆图谱">
		{#snippet badge()}
			<Badge tone="lilac" size="sm">FTS5 + 向量</Badge>
			{#if dreaming?.running}
				<Badge tone="mint" size="sm" dot pulse>Dreaming 巩固中</Badge>
			{/if}
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void loadMemories()}>
				<RefreshCw class={cn('size-3.5', loading && 'animate-spin')} />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" loading={dreamingLoading} onclick={() => void triggerDreaming()}>
				<Sparkles class="size-3.5" />
				<span>运行 Dreaming</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Live Dreaming Result Toast / Status Strip -->
	{#if dreamToast || dreaming}
		<div class="flex flex-wrap items-center justify-between gap-2.5">
			{#if dreaming}
				<div class="flex flex-wrap items-center gap-2 rounded-[var(--radius-panel)] bg-tint-lilac px-3.5 py-2 text-xs text-ink-primary">
					<Sparkles class="size-3.5 shrink-0 text-lilac-600" />
					<span class="font-semibold text-lilac-600">Dreaming</span>
					<span class="font-mono text-ink-secondary">待巩固 {dreaming.pending_episodes}</span>
					<span class="text-ink-muted">·</span>
					<span class="font-mono text-ink-secondary">已归并 {dreaming.consolidated_count}</span>
					{#if dreaming.last_run_at}
						<span class="text-ink-muted">·</span>
						<span class="font-mono text-ink-muted">上次 {fmtClock(dreaming.last_run_at)}</span>
					{/if}
				</div>
			{:else}
				<div></div>
			{/if}

			{#if dreamToast}
				<div
					role="status"
					class={cn(
						'animate-pop-in inline-flex items-center gap-2 rounded-[var(--radius-pill)] px-3.5 py-1.5 text-xs font-medium shadow-[var(--shadow-xs)]',
						dreamToast.tone === 'mint'
							? 'bg-tint-mint text-mint-700'
							: 'bg-tint-rose text-rose-600'
					)}
				>
					<CheckCircle2 class="size-3.5 shrink-0" />
					<span>{dreamToast.text}</span>
					<button
						type="button"
						aria-label="关闭提示"
						onclick={() => (dreamToast = null)}
						class="ml-0.5 cursor-pointer rounded-full p-0.5 hover:bg-black/5"
					>
						<X class="size-3" />
					</button>
				</div>
			{/if}
		</div>
	{/if}

	<!-- Search + Kind Filter + Active/Cold Archive Bar -->
	<SurfaceCard variant="raised" padding="sm" class="space-y-3">
		<form
			class="flex flex-col gap-2.5 sm:flex-row sm:items-center"
			onsubmit={(e) => {
				e.preventDefault();
				void runSearch();
			}}
		>
			<Input
				bind:value={query}
				placeholder="混合检索记忆内容、实体或时间线索..."
				class="flex-1"
			>
				{#snippet leading()}
					<Search class="size-4" />
				{/snippet}
				{#snippet trailing()}
					{#if query}
						<button
							type="button"
							aria-label="清空检索"
							onclick={() => {
								query = '';
								void loadMemories();
							}}
							class="cursor-pointer rounded-full p-0.5 text-ink-muted hover:text-ink-primary"
						>
							<X class="size-3.5" />
						</button>
					{/if}
				{/snippet}
			</Input>

			<div class="flex shrink-0 items-center gap-2">
				<Button type="submit" variant="primary" size="md" {loading}>检索</Button>

				<!-- Active vs Cold Archive segmented toggle -->
				<div class="inline-flex items-center rounded-[var(--radius-control)] bg-surface-subtle p-1">
					<button
						type="button"
						onclick={() => (archiveView = 'active')}
						class={cn(
							'inline-flex cursor-pointer items-center gap-1.5 rounded-[calc(var(--radius-control)-4px)] px-3 py-1.5 text-xs font-medium whitespace-nowrap transition-all',
							archiveView === 'active'
								? 'bg-surface-raised text-ink-primary shadow-[var(--shadow-xs)]'
								: 'text-ink-secondary hover:text-ink-primary'
						)}
					>
						<span>活跃</span>
						<span class="font-mono text-[11px] text-ink-muted">{activeCount}</span>
					</button>
					<button
						type="button"
						onclick={() => (archiveView = 'archived')}
						class={cn(
							'inline-flex cursor-pointer items-center gap-1.5 rounded-[calc(var(--radius-control)-4px)] px-3 py-1.5 text-xs font-medium whitespace-nowrap transition-all',
							archiveView === 'archived'
								? 'bg-surface-raised text-ink-primary shadow-[var(--shadow-xs)]'
								: 'text-ink-secondary hover:text-ink-primary'
						)}
					>
						<Archive class="size-3" />
						<span>冷归档</span>
						<span class="font-mono text-[11px] text-ink-muted">{archivedCount}</span>
					</button>
				</div>
			</div>
		</form>

		<div class="flex flex-wrap items-center justify-between gap-2">
			<div class="flex flex-wrap items-center gap-1.5">
				{#each kindPills as pill (pill.key)}
					{@const active = kindFilter === pill.key}
					<button
						type="button"
						onclick={() => (kindFilter = pill.key)}
						class={cn(
							'cursor-pointer rounded-[var(--radius-pill)] px-3 py-1 text-xs font-medium whitespace-nowrap transition-all duration-150',
							active
								? 'bg-ink-primary text-white shadow-[var(--shadow-xs)]'
								: 'bg-surface-subtle text-ink-secondary hover:bg-surface-muted hover:text-ink-primary'
						)}
					>
						{pill.label}
					</button>
				{/each}

				{#if selectedEntity}
					<button
						type="button"
						onclick={() => (selectedEntity = null)}
						class="inline-flex cursor-pointer items-center gap-1 rounded-[var(--radius-pill)] bg-tint-mint px-3 py-1 text-xs font-semibold text-mint-700"
					>
						<Hash class="size-3" />
						<span>{selectedEntity}</span>
						<X class="size-3" />
					</button>
				{/if}
			</div>

			{#if activeQuery}
				<div class="flex items-center gap-2 font-mono text-[11px] text-ink-muted">
					<span>“{activeQuery}” · {filteredItems.length} 命中</span>
					{#if searchElapsedMs !== null}
						<Badge tone="mint" size="sm" class="font-mono">{searchElapsedMs}ms</Badge>
					{/if}
				</div>
			{/if}
		</div>
	</SurfaceCard>

	<!-- Main Content Grid: Memory Cards + Compact Entity Alias Chips Panel -->
	<div class="grid gap-4 lg:grid-cols-12">
		<!-- Memory Cards List -->
		<div class="space-y-3 lg:col-span-8">
			{#if filteredItems.length === 0}
				<EmptyState
					title={archiveView === 'archived' ? '暂无冷归档记忆' : '暂无匹配记忆条目'}
					hint={selectedEntity
						? `当前实体「${selectedEntity}」下无记录`
						: '群聊蒸馏或触发 Dreaming 后自动收录'}
					tone="lilac"
				>
					{#snippet icon()}
						<Brain class="size-5" />
					{/snippet}
					{#snippet action()}
						{#if selectedEntity || kindFilter !== 'all' || activeQuery}
							<Button
								variant="tonal"
								size="sm"
								onclick={() => {
									selectedEntity = null;
									kindFilter = 'all';
									query = '';
									void loadMemories();
								}}
							>
								<RotateCcw class="size-3.5" />
								<span>重置筛选</span>
							</Button>
						{/if}
					{/snippet}
				</EmptyState>
			{:else}
				{#each filteredItems as item (item.id)}
					{@const km = kindMeta(item.kind)}
					{@const replacedCount = supersededCountByTarget.get(item.id) ?? 0}
					{@const isSuperseded = Boolean(item.superseded_by)}
					{@const isHighlighted = highlightedId === item.id}
					<SurfaceCard
						id={`mem-${item.id}`}
						variant={isHighlighted ? 'tint-mint' : 'raised'}
						padding="md"
						class={cn('space-y-3 transition-all duration-300', isSuperseded && 'opacity-75')}
					>
						<!-- Top Meta Badges Row -->
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex flex-wrap items-center gap-1.5">
								<Badge tone={km.tone} size="sm">{km.label}</Badge>
								<span class="inline-flex items-center gap-1 rounded-[var(--radius-pill)] bg-surface-subtle px-2.5 py-0.5 font-mono text-[11px] font-medium text-ink-secondary">
									<Calendar class="size-3 text-ink-muted" />
									[{formatEventDate(item)}]
								</span>
								{#if isArchivedItem(item)}
									<Badge tone="neutral" size="sm">冷归档</Badge>
								{/if}
								{#if replacedCount > 0}
									<Badge tone="mint" size="sm">
										<GitBranch class="size-3" />
										<span>更新自 {replacedCount} 条旧记忆</span>
									</Badge>
								{/if}
							</div>

							<div class="flex items-center gap-1.5 font-mono text-[11px]">
								<span class="inline-flex items-center gap-1 rounded-full bg-tint-amber px-2 py-0.5 font-semibold text-amber-600">
									<Flame class="size-3" />
									{formatImportance(item)}
								</span>
								<span class="rounded-full bg-surface-subtle px-2 py-0.5 text-ink-secondary">
									召回 {item.recall_count}
								</span>
							</div>
						</div>

						<!-- Memory Content -->
						<p
							class={cn(
								'text-sm leading-relaxed text-ink-primary',
								isSuperseded && 'line-through decoration-ink-muted/60'
							)}
						>
							{item.content}
						</p>

						<!-- Temporal Superseded Chain Indicator -->
						{#if item.superseded_by}
							<div class="flex flex-wrap items-center justify-between gap-2 rounded-[var(--radius-control)] bg-tint-amber px-3 py-2 text-xs text-amber-600">
								<div class="flex min-w-0 items-center gap-1.5">
									<GitBranch class="size-3.5 shrink-0" />
									<span class="font-medium">时序已取代</span>
									<ArrowRight class="size-3 shrink-0" />
									<span class="truncate font-mono font-semibold">{item.superseded_by}</span>
								</div>
								<button
									type="button"
									onclick={() => jumpToMemory(item.superseded_by!)}
									class="shrink-0 cursor-pointer rounded-full bg-surface-raised px-2.5 py-0.5 text-[11px] font-semibold text-ink-primary shadow-[var(--shadow-xs)] hover:bg-surface-subtle"
								>
									定位新版本
								</button>
							</div>
						{/if}

						<!-- Canonical Entity Tags -->
						{#if item.entities && item.entities.length > 0}
							<div class="flex flex-wrap items-center gap-1.5">
								{#each item.entities as entity (entity)}
									{@const activeEnt = selectedEntity === entity}
									<button
										type="button"
										onclick={() => (selectedEntity = activeEnt ? null : entity)}
										class={cn(
											'inline-flex cursor-pointer items-center gap-1 rounded-[var(--radius-pill)] px-2.5 py-0.5 text-[11px] font-medium transition-colors',
											activeEnt
												? 'bg-mint-500 text-white'
												: 'bg-surface-subtle text-ink-secondary hover:bg-tint-mint hover:text-mint-700'
										)}
									>
										<Hash class="size-2.5" />
										<span>{entity}</span>
									</button>
								{/each}
							</div>
						{/if}
					</SurfaceCard>
				{/each}
			{/if}
		</div>

		<!-- Compact Entity Alias Chips Panel -->
		<SurfaceCard variant="raised" padding="md" class="h-fit space-y-3 lg:col-span-4">
			<div class="flex items-center justify-between gap-2">
				<div class="flex items-center gap-2">
					<span class="flex size-7 items-center justify-center rounded-xl bg-tint-mint text-mint-600">
						<Tag class="size-3.5" />
					</span>
					<span class="text-sm font-semibold text-ink-primary">实体与别名</span>
				</div>
				<Badge tone="neutral" size="sm">{aliasGroups.length}</Badge>
			</div>

			{#if aliasGroups.length === 0}
				<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-6 text-center text-xs text-ink-muted">
					暂无实体别名映射
				</div>
			{:else}
				<div class="space-y-2">
					{#each aliasGroups as group (group.canonical)}
						{@const isSelected = selectedEntity === group.canonical}
						<div
							class={cn(
								'rounded-[var(--radius-panel)] p-3 transition-colors',
								isSelected ? 'bg-tint-mint' : 'bg-surface-subtle'
							)}
						>
							<button
								type="button"
								onclick={() => (selectedEntity = isSelected ? null : group.canonical)}
								class="flex w-full cursor-pointer items-center justify-between gap-2 text-left"
							>
								<span class="inline-flex min-w-0 items-center gap-1.5 text-xs font-semibold text-ink-primary">
									<Hash class="size-3 shrink-0 text-mint-600" />
									<span class="truncate">{group.canonical}</span>
								</span>
								<span class="shrink-0 rounded-full bg-surface-raised px-2 py-0.5 font-mono text-[10px] font-semibold text-ink-secondary">
									{group.memoryCount}
								</span>
							</button>

							{#if group.aliases.length > 0}
								<div class="mt-2 flex flex-wrap gap-1">
									{#each group.aliases as alias (alias)}
										<span class="rounded-full bg-surface-raised px-2 py-0.5 text-[11px] text-ink-secondary">
											{alias}
										</span>
									{/each}
								</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}
		</SurfaceCard>
	</div>
</div>
