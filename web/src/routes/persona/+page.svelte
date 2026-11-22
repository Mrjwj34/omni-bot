<script lang="ts">
	import { Spring } from 'svelte/motion';
	import {
		ArrowRight,
		Check,
		GitCompareArrows,
		HeartHandshake,
		History,
		Lock,
		Pencil,
		Plus,
		RefreshCw,
		RotateCcw,
		Save,
		SlidersHorizontal,
		Sparkles,
		Trash2,
		X
	} from '@lucide/svelte';
	import {
		api,
		type PADTemperament,
		type PersonaResponse,
		type PersonaSnapshot,
		type PersonaTrait
	} from '$lib/api';
	import {
		Badge,
		Button,
		Dialog,
		EmptyState,
		Input,
		PageHeader,
		SurfaceCard,
		Tabs,
		Textarea,
		cn,
		type BadgeTone,
		type TabItem
	} from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	const MAX_DRIFT = 0.15;
	const MIN_TRAITS = 8;
	const MAX_TRAITS = 12;

	const DEFAULT_TEMPERAMENT: PADTemperament = {
		initial_baseline: { p: 0.12, a: 0.02, d: 0.02 },
		baseline: { p: 0.12, a: 0.02, d: 0.02 },
		sensitivity: { p: 0.18, a: 0.22, d: 0.18 },
		half_life_p_ms: 12 * 60 * 1000,
		half_life_a_ms: 6 * 60 * 1000,
		half_life_d_ms: 10 * 60 * 1000,
		deadzone_radius: 0.18
	};

	type AxisKey = 'p' | 'a' | 'd';

	interface TraitDiffEntry {
		id: string;
		before: string;
		after: string;
	}

	interface ComputedSnapshotDiff {
		beliefsChanged: boolean;
		beliefsBefore: string;
		beliefsAfter: string;
		added: PersonaTrait[];
		removed: PersonaTrait[];
		changed: TraitDiffEntry[];
		unchanged: PersonaTrait[];
	}

	function cloneTemperament(t: PADTemperament): PADTemperament {
		return {
			initial_baseline: { ...t.initial_baseline },
			baseline: { ...t.baseline },
			sensitivity: { ...t.sensitivity },
			half_life_p_ms: t.half_life_p_ms,
			half_life_a_ms: t.half_life_a_ms,
			half_life_d_ms: t.half_life_d_ms,
			deadzone_radius: t.deadzone_radius
		};
	}

	function normalizeTrait(raw: PersonaTrait & { content?: string }): PersonaTrait {
		return {
			id: raw.id,
			text: raw.text ?? raw.content ?? '',
			weight: typeof raw.weight === 'number' ? raw.weight : 1,
			updated_at: raw.updated_at ?? ''
		};
	}

	let tab = $state('core');
	let loading = $state(false);
	let savingBeliefs = $state(false);
	let savingTemp = $state(false);
	let savingTraits = $state(false);
	let reflecting = $state(false);
	let rollingBack = $state(false);

	let notice = $state<{ tone: BadgeTone; text: string } | null>(null);
	let noticeTimer: ReturnType<typeof setTimeout> | null = null;

	let coreBeliefs = $state('');
	let beliefsDraft = $state('');
	let temperament = $state<PADTemperament>(cloneTemperament(DEFAULT_TEMPERAMENT));
	let tempDraft = $state<PADTemperament>(cloneTemperament(DEFAULT_TEMPERAMENT));
	let traits = $state<PersonaTrait[]>([]);
	let savedTraitsSnapshot = $state<string>('[]');
	let snapshots = $state.raw<PersonaSnapshot[]>([]);

	let newTraitText = $state('');
	let editingTraitId = $state<string | null>(null);
	let editingTraitDraft = $state('');

	let selectedToVersion = $state<number | null>(null);
	let selectedFromVersion = $state<number | null>(null);
	let showUnchanged = $state(false);
	let rollbackDialogOpen = $state(false);
	let rollbackTarget = $state<PersonaSnapshot | null>(null);

	const livePad = Spring.of(
		() => ({
			p: realtime.padState.value.p,
			a: realtime.padState.value.a,
			d: realtime.padState.value.d
		}),
		{ stiffness: 0.14, damping: 0.48 }
	);

	let beliefsDirty = $derived(beliefsDraft !== coreBeliefs);
	let tempDirty = $derived(JSON.stringify(tempDraft) !== JSON.stringify(temperament));
	let traitsDirty = $derived(JSON.stringify(traits) !== savedTraitsSnapshot);

	let traitCountTone = $derived<BadgeTone>(
		traits.length > MAX_TRAITS ? 'rose' : traits.length >= MIN_TRAITS ? 'mint' : 'amber'
	);

	let sortedSnapshots = $derived(
		[...snapshots].sort((a, b) => b.version - a.version)
	);

	let activeToSnapshot = $derived.by(() => {
		if (sortedSnapshots.length === 0) return null;
		return (
			sortedSnapshots.find((s) => s.version === selectedToVersion) ?? sortedSnapshots[0]
		);
	});

	let activeFromSnapshot = $derived.by(() => {
		if (sortedSnapshots.length === 0) return null;
		if (selectedFromVersion !== null) {
			const match = sortedSnapshots.find((s) => s.version === selectedFromVersion);
			if (match) return match;
		}
		if (!activeToSnapshot) return null;
		const idx = sortedSnapshots.findIndex((s) => s.version === activeToSnapshot.version);
		return sortedSnapshots[idx + 1] ?? activeToSnapshot;
	});

	let snapshotDiff = $derived.by<ComputedSnapshotDiff | null>(() => {
		const fromSnap = activeFromSnapshot;
		const toSnap = activeToSnapshot;
		if (!fromSnap || !toSnap) return null;

		const fromMap = new Map(fromSnap.traits.map((t) => [t.id, t]));
		const toMap = new Map(toSnap.traits.map((t) => [t.id, t]));

		const added: PersonaTrait[] = [];
		const changed: TraitDiffEntry[] = [];
		const unchanged: PersonaTrait[] = [];
		const removed: PersonaTrait[] = [];

		for (const tr of toSnap.traits) {
			const prev = fromMap.get(tr.id);
			if (!prev) {
				added.push(tr);
			} else if (prev.text !== tr.text) {
				changed.push({ id: tr.id, before: prev.text, after: tr.text });
			} else {
				unchanged.push(tr);
			}
		}

		for (const tr of fromSnap.traits) {
			if (!toMap.has(tr.id)) {
				removed.push(tr);
			}
		}

		return {
			beliefsChanged: fromSnap.core_beliefs !== toSnap.core_beliefs,
			beliefsBefore: fromSnap.core_beliefs,
			beliefsAfter: toSnap.core_beliefs,
			added,
			removed,
			changed,
			unchanged
		};
	});

	let liveDistanceFromBaseline = $derived(
		Math.hypot(
			livePad.current.p - tempDraft.baseline.p,
			livePad.current.a - tempDraft.baseline.a,
			livePad.current.d - tempDraft.baseline.d
		)
	);

	let inDeadzone = $derived(liveDistanceFromBaseline <= tempDraft.deadzone_radius);

	let tabs = $derived<TabItem[]>([
		{ value: 'core', label: '冻结核心 · 气质' },
		{ value: 'traits', label: '原子性格', badge: `${traits.length}/${MAX_TRAITS}` },
		{ value: 'snapshots', label: '版本快照 Diff', badge: sortedSnapshots.length }
	]);

	const axisMeta: {
		key: AxisKey;
		label: string;
		short: string;
		tauField: 'half_life_p_ms' | 'half_life_a_ms' | 'half_life_d_ms';
		envelopeClass: string;
		dotClass: string;
		badgeTone: BadgeTone;
	}[] = [
		{
			key: 'p',
			label: 'P 愉悦',
			short: 'P',
			tauField: 'half_life_p_ms',
			envelopeClass: 'bg-mint-400/30',
			dotClass: 'bg-mint-500',
			badgeTone: 'mint'
		},
		{
			key: 'a',
			label: 'A 唤醒',
			short: 'A',
			tauField: 'half_life_a_ms',
			envelopeClass: 'bg-coral-400/30',
			dotClass: 'bg-coral-500',
			badgeTone: 'coral'
		},
		{
			key: 'd',
			label: 'D 掌控',
			short: 'D',
			tauField: 'half_life_d_ms',
			envelopeClass: 'bg-lilac-400/30',
			dotClass: 'bg-lilac-500',
			badgeTone: 'lilac'
		}
	];

	function flash(tone: BadgeTone, text: string): void {
		notice = { tone, text };
		if (noticeTimer !== null) clearTimeout(noticeTimer);
		noticeTimer = setTimeout(() => {
			notice = null;
			noticeTimer = null;
		}, 3400);
	}

	function applyPersonaResponse(data: PersonaResponse): void {
		coreBeliefs = data.core_beliefs ?? '';
		beliefsDraft = coreBeliefs;
		if (data.temperament) {
			temperament = cloneTemperament(data.temperament);
			tempDraft = cloneTemperament(data.temperament);
		}
		const normalizedTraits = (data.traits ?? []).map(normalizeTrait);
		traits = normalizedTraits;
		savedTraitsSnapshot = JSON.stringify(normalizedTraits);
		snapshots = (data.snapshots ?? []).map((snap) => ({
			...snap,
			traits: (snap.traits ?? []).map(normalizeTrait)
		}));
		if (data.pad) {
			realtime.seedPAD(data.pad);
		}
		if (snapshots.length > 0) {
			const newest = [...snapshots].sort((a, b) => b.version - a.version);
			if (selectedToVersion === null || !newest.some((s) => s.version === selectedToVersion)) {
				selectedToVersion = newest[0].version;
				selectedFromVersion = newest[1]?.version ?? newest[0].version;
			}
		}
	}

	async function loadPersona(signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const data = await api.getPersona({ signal });
			applyPersonaResponse(data);
		} catch {
			// Standalone preview or offline kernel: keep clean interactive standby state.
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void loadPersona(ctrl.signal);
		return () => {
			ctrl.abort();
			if (noticeTimer !== null) clearTimeout(noticeTimer);
		};
	});

	async function saveBeliefs(): Promise<void> {
		savingBeliefs = true;
		try {
			const updated = await api.updatePersonaBeliefs(beliefsDraft.trim());
			applyPersonaResponse(updated);
			flash('mint', '核心信念已保存');
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '保存信念失败');
		} finally {
			savingBeliefs = false;
		}
	}

	async function saveTemperament(): Promise<void> {
		savingTemp = true;
		try {
			const updated = await api.updateTemperament(cloneTemperament(tempDraft));
			applyPersonaResponse(updated);
			flash('mint', '气质参数已更新');
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '更新气质失败');
		} finally {
			savingTemp = false;
		}
	}

	function addTrait(): void {
		const text = newTraitText.trim();
		if (!text || traits.length >= MAX_TRAITS) return;
		const next: PersonaTrait = {
			id: `trait_${Date.now().toString(36)}`,
			text,
			weight: 1,
			updated_at: new Date().toISOString()
		};
		traits = [...traits, next];
		newTraitText = '';
	}

	function startEditTrait(tr: PersonaTrait): void {
		editingTraitId = tr.id;
		editingTraitDraft = tr.text;
	}

	function commitEditTrait(): void {
		if (!editingTraitId) return;
		const trimmed = editingTraitDraft.trim();
		if (!trimmed) return;
		const now = new Date().toISOString();
		traits = traits.map((tr) =>
			tr.id === editingTraitId ? { ...tr, text: trimmed, updated_at: now } : tr
		);
		editingTraitId = null;
		editingTraitDraft = '';
	}

	function removeTrait(id: string): void {
		traits = traits.filter((tr) => tr.id !== id);
		if (editingTraitId === id) {
			editingTraitId = null;
			editingTraitDraft = '';
		}
	}

	async function saveTraits(): Promise<void> {
		savingTraits = true;
		try {
			const res = await fetch('/api/persona/traits', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ traits })
			});
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			const updated = (await res.json()) as PersonaResponse;
			if (updated && typeof updated === 'object' && 'traits' in updated) {
				applyPersonaResponse(updated);
			} else {
				savedTraitsSnapshot = JSON.stringify(traits);
			}
			flash('mint', '性格条目已保存');
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '保存性格条目失败');
		} finally {
			savingTraits = false;
		}
	}

	async function triggerReflect(): Promise<void> {
		reflecting = true;
		try {
			const res = await fetch('/api/persona/reflect', { method: 'POST' });
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			flash('lilac', '已触发人格反思');
			await loadPersona();
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '触发反思失败');
		} finally {
			reflecting = false;
		}
	}

	function openRollbackDialog(snap: PersonaSnapshot): void {
		rollbackTarget = snap;
		rollbackDialogOpen = true;
	}

	async function confirmRollback(): Promise<void> {
		if (!rollbackTarget) return;
		rollingBack = true;
		const version = rollbackTarget.version;
		try {
			const res = await fetch(
				`/api/persona/snapshots/${encodeURIComponent(String(version))}/rollback`,
				{ method: 'POST' }
			);
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			rollbackDialogOpen = false;
			rollbackTarget = null;
			flash('mint', `已回滚至快照 v${version}`);
			await loadPersona();
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '回滚快照失败');
		} finally {
			rollingBack = false;
		}
	}

	function selectToSnapshot(snap: PersonaSnapshot): void {
		selectedToVersion = snap.version;
		const idx = sortedSnapshots.findIndex((s) => s.version === snap.version);
		selectedFromVersion = sortedSnapshots[idx + 1]?.version ?? snap.version;
	}

	function axisPct(v: number): number {
		return Math.max(0, Math.min(100, ((v + 1) / 2) * 100));
	}

	function envelopeRange(initialVal: number): { left: number; width: number } {
		const lo = Math.max(-1, initialVal - MAX_DRIFT);
		const hi = Math.min(1, initialVal + MAX_DRIFT);
		const left = axisPct(lo);
		const right = axisPct(hi);
		return { left, width: Math.max(2, right - left) };
	}

	function fmtSigned(v: number): string {
		const sign = v > 0 ? '+' : '';
		return `${sign}${v.toFixed(2)}`;
	}

	function msToMin(ms: number): number {
		return Math.max(1, Math.round(ms / 60000));
	}

	function setTauMin(field: 'half_life_p_ms' | 'half_life_a_ms' | 'half_life_d_ms', minutes: number): void {
		tempDraft[field] = Math.max(1, Math.min(60, minutes)) * 60000;
	}

	function formatDateTime(iso: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return iso;
		return d.toLocaleString('zh-CN', {
			month: '2-digit',
			day: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
			hour12: false
		});
	}

	function reasonTone(reason: string): BadgeTone {
		const r = reason.toLowerCase();
		if (r.includes('rollback')) return 'amber';
		if (r.includes('reflect')) return 'lilac';
		if (r.includes('temperament')) return 'coral';
		return 'sky';
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="人格情绪">
		{#snippet badge()}
			<div class="flex flex-wrap items-center gap-2">
				<Badge tone={realtime.moodTone} size="sm" dot pulse={realtime.connectionStatus === 'connected'}>
					{realtime.moodLabel}
				</Badge>
				{#if notice}
					<Badge tone={notice.tone} size="sm">{notice.text}</Badge>
				{/if}
			</div>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void loadPersona()}>
				<RefreshCw class="size-3.5" />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" loading={reflecting} onclick={() => void triggerReflect()}>
				<Sparkles class="size-3.5" />
				<span>立即反思</span>
			</Button>
		{/snippet}
	</PageHeader>

	<Tabs bind:value={tab} items={tabs}>
		{#snippet children(activeTab)}
			{#if activeTab === 'core'}
				<div class="grid gap-4 lg:grid-cols-12">
					<!-- Left: Frozen Core Beliefs + Live Inner Feeling -->
					<div class="flex flex-col gap-4 lg:col-span-7">
						<SurfaceCard variant="raised" padding="lg" class="flex flex-1 flex-col gap-4">
							<div class="flex flex-wrap items-center justify-between gap-2">
								<div class="flex items-center gap-2.5">
									<span class="flex size-8 shrink-0 items-center justify-center rounded-xl bg-tint-lilac text-lilac-600">
										<Lock class="size-4" />
									</span>
									<h2 class="text-sm font-semibold text-ink-primary">《自我观念与信念》</h2>
									<Badge tone="lilac" size="sm">冻结锚点</Badge>
								</div>
								{#if beliefsDirty}
									<Badge tone="amber" size="sm" dot>未保存</Badge>
								{/if}
							</div>

							<Textarea
								bind:value={beliefsDraft}
								rows={11}
								placeholder="以第一人称“我”写下核心价值与自我认知..."
								class="min-h-[220px] flex-1 leading-relaxed"
							/>

							<div class="flex flex-wrap items-center justify-between gap-3">
								<span class="font-mono text-xs text-ink-muted">
									{beliefsDraft.trim().length} 字 · 第一人称主体
								</span>
								<div class="flex items-center gap-2">
									{#if beliefsDirty}
										<Button
											variant="ghost"
											size="sm"
											onclick={() => (beliefsDraft = coreBeliefs)}
										>
											重置
										</Button>
									{/if}
									<Button
										variant="primary"
										size="sm"
										disabled={!beliefsDirty}
										loading={savingBeliefs}
										onclick={() => void saveBeliefs()}
									>
										<Save class="size-3.5" />
										<span>保存信念</span>
									</Button>
								</div>
							</div>
						</SurfaceCard>

						<SurfaceCard variant={inDeadzone ? 'subtle' : 'tint-mint'} padding="md">
							<div class="flex flex-wrap items-center justify-between gap-2">
								<div class="flex items-center gap-2">
									<HeartHandshake class="size-4 text-mint-600" />
									<span class="text-xs font-semibold text-ink-primary">当下内在感受</span>
								</div>
								<Badge tone={inDeadzone ? 'neutral' : realtime.moodTone} size="sm">
									{inDeadzone ? '中性死区 · 静默' : realtime.moodLabel}
								</Badge>
							</div>
							<p class="mt-2.5 text-sm leading-relaxed text-ink-secondary">
								{#if realtime.padState.feeling?.trim()}
									{realtime.padState.feeling}
								{:else if inDeadzone}
									坐标落在静息死区半径内，系统不注入情绪块。
								{:else}
									P {fmtSigned(livePad.current.p)} · A {fmtSigned(livePad.current.a)} · D {fmtSigned(livePad.current.d)}
								{/if}
							</p>
						</SurfaceCard>
					</div>

					<!-- Right: Temperament PAD Gauges with Hard-Envelope Drift Bar -->
					<SurfaceCard variant="raised" padding="lg" class="flex flex-col gap-4 lg:col-span-5">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex items-center gap-2.5">
								<span class="flex size-8 shrink-0 items-center justify-center rounded-xl bg-tint-peach text-coral-600">
									<SlidersHorizontal class="size-4" />
								</span>
								<h2 class="text-sm font-semibold text-ink-primary">气质动力学 PAD</h2>
							</div>
							<Badge tone="coral" size="sm">±{MAX_DRIFT.toFixed(2)} 硬包络</Badge>
						</div>

						<div class="space-y-3.5">
							{#each axisMeta as axis (axis.key)}
								{@const initVal = tempDraft.initial_baseline[axis.key]}
								{@const baseVal = tempDraft.baseline[axis.key]}
								{@const liveVal = livePad.current[axis.key]}
								{@const env = envelopeRange(initVal)}
								{@const drift = baseVal - initVal}
								<div class="space-y-2.5 rounded-[var(--radius-panel)] bg-surface-subtle p-3.5">
									<div class="flex flex-wrap items-center justify-between gap-2">
										<div class="flex items-center gap-2">
											<span class="text-xs font-semibold text-ink-primary">{axis.label}</span>
											<Badge tone={axis.badgeTone} size="sm">
												Δ{fmtSigned(drift)}
											</Badge>
										</div>
										<div class="flex items-center gap-2.5 font-mono text-[11px] text-ink-muted">
											<span>锚 {fmtSigned(initVal)}</span>
											<span class="text-ink-primary font-semibold">实时 {fmtSigned(liveVal)}</span>
										</div>
									</div>

									<!-- Hard-envelope bar: [-1, +1] track, ±0.15 band, frozen anchor, baseline, live Spring dot -->
									<div class="relative flex h-5 items-center">
										<div class="relative h-2.5 w-full rounded-full bg-surface-muted">
											<div
												class="absolute top-0 bottom-0 left-1/2 w-px -translate-x-1/2 bg-ink-muted/30"
											></div>
											<div
												class={cn('absolute top-0 bottom-0 rounded-full', axis.envelopeClass)}
												style:left={`${env.left}%`}
												style:width={`${env.width}%`}
											></div>
											<div
												title={`冻结锚点 ${fmtSigned(initVal)}`}
												class="absolute -top-0.5 -bottom-0.5 w-0.5 -translate-x-1/2 rounded-full bg-ink-secondary/75"
												style:left={`${axisPct(initVal)}%`}
											></div>
											<div
												title={`当前基线 ${fmtSigned(baseVal)}`}
												class={cn(
													'absolute top-1/2 size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full',
													axis.dotClass
												)}
												style:left={`${axisPct(baseVal)}%`}
											></div>
											<div
												title={`实时状态 ${fmtSigned(liveVal)}`}
												class="absolute top-1/2 flex size-4 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full bg-surface-raised shadow-[var(--shadow-xs)]"
												style:left={`${axisPct(liveVal)}%`}
											>
												<span class={cn('size-2 rounded-full', axis.dotClass)}></span>
											</div>
										</div>
									</div>

									<div class="grid grid-cols-3 gap-2 pt-0.5">
										<label class="flex flex-col gap-1">
											<span class="flex items-center justify-between text-[11px] text-ink-muted">
												<span>基线</span>
												<span class="font-mono text-ink-primary">{fmtSigned(baseVal)}</span>
											</span>
											<input
												type="range"
												min={Math.max(-1, +(initVal - MAX_DRIFT).toFixed(2))}
												max={Math.min(1, +(initVal + MAX_DRIFT).toFixed(2))}
												step="0.01"
												value={baseVal}
												oninput={(e) => {
													tempDraft.baseline[axis.key] = Number(e.currentTarget.value);
												}}
												class="h-1.5 w-full cursor-pointer accent-mint-500"
											/>
										</label>

										<label class="flex flex-col gap-1">
											<span class="flex items-center justify-between text-[11px] text-ink-muted">
												<span>η 敏感</span>
												<span class="font-mono text-ink-primary">
													{tempDraft.sensitivity[axis.key].toFixed(2)}
												</span>
											</span>
											<input
												type="range"
												min="0.05"
												max="0.60"
												step="0.01"
												value={tempDraft.sensitivity[axis.key]}
												oninput={(e) => {
													tempDraft.sensitivity[axis.key] = Number(e.currentTarget.value);
												}}
												class="h-1.5 w-full cursor-pointer accent-coral-500"
											/>
										</label>

										<label class="flex flex-col gap-1">
											<span class="flex items-center justify-between text-[11px] text-ink-muted">
												<span>τ 半衰</span>
												<span class="font-mono text-ink-primary">
													{msToMin(tempDraft[axis.tauField])}m
												</span>
											</span>
											<input
												type="range"
												min="1"
												max="60"
												step="1"
												value={msToMin(tempDraft[axis.tauField])}
												oninput={(e) => {
													setTauMin(axis.tauField, Number(e.currentTarget.value));
												}}
												class="h-1.5 w-full cursor-pointer accent-lilac-500"
											/>
										</label>
									</div>
								</div>
							{/each}

							<!-- Deadzone radius -->
							<div class="rounded-[var(--radius-panel)] bg-surface-subtle p-3.5">
								<div class="flex flex-wrap items-center justify-between gap-2 text-xs">
									<span class="font-semibold text-ink-primary">中性死区半径</span>
									<div class="flex items-center gap-2 font-mono text-[11px]">
										<span class="text-ink-muted">‖Δ‖ {liveDistanceFromBaseline.toFixed(2)}</span>
										<Badge tone={inDeadzone ? 'neutral' : 'mint'} size="sm">
											r = {tempDraft.deadzone_radius.toFixed(2)}
										</Badge>
									</div>
								</div>
								<input
									type="range"
									min="0.05"
									max="0.45"
									step="0.01"
									value={tempDraft.deadzone_radius}
									oninput={(e) => {
										tempDraft.deadzone_radius = Number(e.currentTarget.value);
									}}
									class="mt-2.5 h-1.5 w-full cursor-pointer accent-mint-500"
								/>
							</div>
						</div>

						<div class="flex items-center justify-end gap-2 pt-1">
							{#if tempDirty}
								<Button
									variant="ghost"
									size="sm"
									onclick={() => (tempDraft = cloneTemperament(temperament))}
								>
									重置
								</Button>
							{/if}
							<Button
								variant="primary"
								size="sm"
								disabled={!tempDirty}
								loading={savingTemp}
								onclick={() => void saveTemperament()}
							>
								<Save class="size-3.5" />
								<span>应用气质</span>
							</Button>
						</div>
					</SurfaceCard>
				</div>
			{:else if activeTab === 'traits'}
				<div class="space-y-4">
					<!-- Trait Toolbar + Quick Add -->
					<SurfaceCard variant="raised" padding="md">
						<div class="flex flex-wrap items-center justify-between gap-3">
							<div class="flex flex-wrap items-center gap-2">
								<h2 class="text-sm font-semibold text-ink-primary">原子性格清单</h2>
								<Badge tone={traitCountTone} size="sm">
									{traits.length} / {MAX_TRAITS}
								</Badge>
								<Badge tone="neutral" size="sm">约束 {MIN_TRAITS}–{MAX_TRAITS} 条</Badge>
								{#if traitsDirty}
									<Badge tone="amber" size="sm" dot>待保存</Badge>
								{/if}
							</div>

							<div class="flex flex-wrap items-center gap-2">
								{#if traitsDirty}
									<Button
										variant="ghost"
										size="sm"
										onclick={() => {
											traits = JSON.parse(savedTraitsSnapshot) as PersonaTrait[];
											editingTraitId = null;
										}}
									>
										撤销改动
									</Button>
								{/if}
								<Button
									variant="tonal"
									size="sm"
									loading={reflecting}
									onclick={() => void triggerReflect()}
								>
									<Sparkles class="size-3.5" />
									<span>立即反思</span>
								</Button>
								<Button
									variant="primary"
									size="sm"
									disabled={!traitsDirty}
									loading={savingTraits}
									onclick={() => void saveTraits()}
								>
									<Save class="size-3.5" />
									<span>保存条目</span>
								</Button>
							</div>
						</div>

						<form
							class="mt-4 flex flex-col gap-2.5 sm:flex-row"
							onsubmit={(e) => {
								e.preventDefault();
								addTrait();
							}}
						>
							<Input
								bind:value={newTraitText}
								placeholder="以第一人称“我”新增一条性格习惯..."
								disabled={traits.length >= MAX_TRAITS}
								class="flex-1"
							/>
							<Button
								type="submit"
								variant="tonal"
								size="md"
								disabled={!newTraitText.trim() || traits.length >= MAX_TRAITS}
							>
								<Plus class="size-4" />
								<span>新增条目</span>
							</Button>
						</form>
					</SurfaceCard>

					{#if traits.length === 0}
						<EmptyState
							title="暂无原子性格条目"
							hint="可手动添加 8–12 条第一人称习气，或触发反思生成"
							tone="lilac"
						>
							{#snippet icon()}
								<Sparkles class="size-5" />
							{/snippet}
						</EmptyState>
					{:else}
						<div class="grid gap-3 md:grid-cols-2">
							{#each traits as tr, idx (tr.id)}
								{@const isEditing = editingTraitId === tr.id}
								{@const hasFirstPerson = tr.text.includes('我')}
								<SurfaceCard variant="raised" padding="md" interactive={!isEditing}>
									<div class="flex items-start justify-between gap-2">
										<div class="flex flex-wrap items-center gap-1.5">
											<span class="rounded-full bg-tint-lilac px-2 py-0.5 font-mono text-[11px] font-semibold text-lilac-600">
												#{String(idx + 1).padStart(2, '0')}
											</span>
											<span class="font-mono text-[11px] text-ink-muted">{tr.id}</span>
											{#if !hasFirstPerson}
												<Badge tone="amber" size="sm">建议用“我”</Badge>
											{/if}
										</div>

										{#if !isEditing}
											<div class="flex shrink-0 items-center gap-1">
												<Button
													variant="ghost"
													size="icon"
													aria-label="编辑条目"
													onclick={() => startEditTrait(tr)}
												>
													<Pencil class="size-3.5" />
												</Button>
												<Button
													variant="ghost"
													size="icon"
													aria-label="删除条目"
													onclick={() => removeTrait(tr.id)}
												>
													<Trash2 class="size-3.5 text-rose-500" />
												</Button>
											</div>
										{/if}
									</div>

									{#if isEditing}
										<div class="mt-3 space-y-2.5">
											<Textarea bind:value={editingTraitDraft} rows={3} />
											<div class="flex items-center justify-end gap-2">
												<Button
													variant="ghost"
													size="sm"
													onclick={() => {
														editingTraitId = null;
														editingTraitDraft = '';
													}}
												>
													<X class="size-3.5" />
													<span>取消</span>
												</Button>
												<Button
													variant="primary"
													size="sm"
													disabled={!editingTraitDraft.trim()}
													onclick={commitEditTrait}
												>
													<Check class="size-3.5" />
													<span>完成</span>
												</Button>
											</div>
										</div>
									{:else}
										<p class="mt-2.5 text-sm leading-relaxed text-ink-primary">
											{tr.text}
										</p>
										{#if tr.updated_at}
											<div class="mt-3 font-mono text-[11px] text-ink-muted">
												{formatDateTime(tr.updated_at)}
											</div>
										{/if}
									{/if}
								</SurfaceCard>
							{/each}
						</div>
					{/if}
				</div>
			{:else if activeTab === 'snapshots'}
				{#if sortedSnapshots.length === 0}
					<EmptyState
						title="暂无版本快照"
						hint="保存信念、修改性格或执行反思后将自动留存不可变快照"
						tone="sky"
					>
						{#snippet icon()}
							<History class="size-5" />
						{/snippet}
					</EmptyState>
				{:else}
					<div class="grid gap-4 lg:grid-cols-12">
						<!-- Left: Snapshot Timeline -->
						<SurfaceCard variant="raised" padding="md" class="lg:col-span-4">
							<div class="mb-3 flex items-center justify-between">
								<span class="text-sm font-semibold text-ink-primary">版本时间线</span>
								<Badge tone="neutral" size="sm">{sortedSnapshots.length}</Badge>
							</div>

							<div class="space-y-2">
								{#each sortedSnapshots as snap (snap.version)}
									{@const isSelected = activeToSnapshot?.version === snap.version}
									<button
										type="button"
										onclick={() => selectToSnapshot(snap)}
										class={cn(
											'w-full cursor-pointer rounded-[var(--radius-panel)] p-3 text-left transition-all duration-150',
											isSelected
												? 'bg-tint-mint shadow-[var(--shadow-xs)]'
												: 'bg-surface-subtle hover:bg-surface-muted/80'
										)}
									>
										<div class="flex items-center justify-between gap-2">
											<div class="flex items-center gap-2">
												<span class="font-mono text-xs font-bold text-ink-primary">
													v{snap.version}
												</span>
												<Badge tone={reasonTone(snap.reason)} size="sm">
													{snap.reason || 'snapshot'}
												</Badge>
											</div>
											<span class="font-mono text-[11px] text-ink-muted">
												{snap.traits.length} 条
											</span>
										</div>
										<div class="mt-1.5 font-mono text-[11px] text-ink-secondary">
											{formatDateTime(snap.created_at)}
										</div>
									</button>
								{/each}
							</div>
						</SurfaceCard>

						<!-- Right: Diff Viewer & Rollback -->
						<SurfaceCard variant="raised" padding="lg" class="space-y-4 lg:col-span-8">
							{#if activeToSnapshot && activeFromSnapshot && snapshotDiff}
								<div class="flex flex-wrap items-center justify-between gap-3">
									<div class="flex flex-wrap items-center gap-2">
										<span class="flex size-8 items-center justify-center rounded-xl bg-tint-sky text-sky-600">
											<GitCompareArrows class="size-4" />
										</span>
										<Badge tone="neutral" size="sm">v{activeFromSnapshot.version}</Badge>
										<ArrowRight class="size-3.5 text-ink-muted" />
										<Badge tone="mint" size="sm">v{activeToSnapshot.version}</Badge>
									</div>

									<Button
										variant="tonal"
										size="sm"
										onclick={() => openRollbackDialog(activeToSnapshot)}
									>
										<RotateCcw class="size-3.5" />
										<span>回滚至 v{activeToSnapshot.version}</span>
									</Button>
								</div>

								<!-- Base version selector pills -->
								{#if sortedSnapshots.length > 1}
									<div class="flex flex-wrap items-center gap-1.5">
										<span class="text-xs text-ink-muted">对比基准</span>
										{#each sortedSnapshots as snap (snap.version)}
											<button
												type="button"
												onclick={() => (selectedFromVersion = snap.version)}
												class={cn(
													'cursor-pointer rounded-full px-2.5 py-0.5 font-mono text-xs font-medium transition-colors',
													activeFromSnapshot.version === snap.version
														? 'bg-sky-500 text-white'
														: 'bg-surface-subtle text-ink-secondary hover:bg-surface-muted'
												)}
											>
												v{snap.version}
											</button>
										{/each}
									</div>
								{/if}

								<!-- Summary delta badges -->
								<div class="flex flex-wrap items-center gap-2">
									<Badge tone="mint" size="sm">+{snapshotDiff.added.length} 新增</Badge>
									<Badge tone="amber" size="sm">~{snapshotDiff.changed.length} 调整</Badge>
									<Badge tone="rose" size="sm">-{snapshotDiff.removed.length} 移除</Badge>
									<Badge tone="neutral" size="sm">{snapshotDiff.unchanged.length} 未变</Badge>
									{#if snapshotDiff.beliefsChanged}
										<Badge tone="lilac" size="sm">核心信念变更</Badge>
									{/if}
								</div>

								{#if !snapshotDiff.beliefsChanged && snapshotDiff.added.length === 0 && snapshotDiff.changed.length === 0 && snapshotDiff.removed.length === 0}
									<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
										两个版本之间无结构差异
									</div>
								{:else}
									<!-- Core Beliefs Diff -->
									{#if snapshotDiff.beliefsChanged}
										<div class="space-y-2">
											<div class="text-xs font-semibold text-ink-secondary">《自我观念与信念》变更</div>
											<div class="grid gap-3 sm:grid-cols-2">
												<div class="rounded-[var(--radius-panel)] bg-tint-rose/70 p-3.5">
													<div class="mb-1.5 font-mono text-[11px] font-semibold text-rose-600">
														− v{activeFromSnapshot.version}
													</div>
													<p class="whitespace-pre-wrap text-xs leading-relaxed text-ink-primary">
														{snapshotDiff.beliefsBefore || '（空）'}
													</p>
												</div>
												<div class="rounded-[var(--radius-panel)] bg-tint-mint/80 p-3.5">
													<div class="mb-1.5 font-mono text-[11px] font-semibold text-mint-700">
														+ v{activeToSnapshot.version}
													</div>
													<p class="whitespace-pre-wrap text-xs leading-relaxed text-ink-primary">
														{snapshotDiff.beliefsAfter || '（空）'}
													</p>
												</div>
											</div>
										</div>
									{/if}

									<!-- Added traits -->
									{#if snapshotDiff.added.length > 0}
										<div class="space-y-2">
											{#each snapshotDiff.added as tr (tr.id)}
												<div class="rounded-[var(--radius-panel)] bg-tint-mint p-3.5">
													<div class="mb-1 flex items-center gap-2 font-mono text-[11px] font-semibold text-mint-700">
														<span>+ 新增</span>
														<span>{tr.id}</span>
													</div>
													<p class="text-sm leading-relaxed text-ink-primary">{tr.text}</p>
												</div>
											{/each}
										</div>
									{/if}

									<!-- Changed traits -->
									{#if snapshotDiff.changed.length > 0}
										<div class="space-y-2">
											{#each snapshotDiff.changed as ch (ch.id)}
												<div class="space-y-2 rounded-[var(--radius-panel)] bg-tint-amber p-3.5">
													<div class="font-mono text-[11px] font-semibold text-amber-600">
														~ 调整 · {ch.id}
													</div>
													<p class="text-xs leading-relaxed text-ink-muted line-through">
														{ch.before}
													</p>
													<p class="text-sm leading-relaxed text-ink-primary">{ch.after}</p>
												</div>
											{/each}
										</div>
									{/if}

									<!-- Removed traits -->
									{#if snapshotDiff.removed.length > 0}
										<div class="space-y-2">
											{#each snapshotDiff.removed as tr (tr.id)}
												<div class="rounded-[var(--radius-panel)] bg-tint-rose p-3.5">
													<div class="mb-1 flex items-center gap-2 font-mono text-[11px] font-semibold text-rose-600">
														<span>− 移除</span>
														<span>{tr.id}</span>
													</div>
													<p class="text-sm leading-relaxed text-ink-secondary line-through">
														{tr.text}
													</p>
												</div>
											{/each}
										</div>
									{/if}
								{/if}

								{#if snapshotDiff.unchanged.length > 0}
									<div class="pt-1">
										<Button
											variant="ghost"
											size="sm"
											onclick={() => (showUnchanged = !showUnchanged)}
										>
											{showUnchanged ? '收起未变更条目' : `展开 ${snapshotDiff.unchanged.length} 条未变更性格`}
										</Button>
										{#if showUnchanged}
											<ul class="mt-2 space-y-1.5">
												{#each snapshotDiff.unchanged as tr (tr.id)}
													<li class="rounded-[var(--radius-control)] bg-surface-subtle px-3 py-2 text-xs text-ink-secondary">
														{tr.text}
													</li>
												{/each}
											</ul>
										{/if}
									</div>
								{/if}
							{/if}
						</SurfaceCard>
					</div>
				{/if}
			{/if}
		{/snippet}
	</Tabs>

	<!-- Rollback Confirmation Dialog -->
	<Dialog bind:open={rollbackDialogOpen} title="确认回滚快照" size="sm">
		{#if rollbackTarget}
			<div class="space-y-3">
				<div class="flex flex-wrap items-center gap-2 rounded-[var(--radius-panel)] bg-tint-amber p-3.5">
					<Badge tone="amber" size="sm">v{rollbackTarget.version}</Badge>
					<span class="text-xs font-medium text-ink-primary">{rollbackTarget.reason || 'snapshot'}</span>
					<span class="ml-auto font-mono text-[11px] text-ink-muted">
						{rollbackTarget.traits.length} 条性格
					</span>
				</div>
				<p class="text-xs leading-relaxed text-ink-secondary">
					将恢复至该版本的信念与性格条目，并自动记录一条新的审计快照。
				</p>
			</div>
		{/if}

		{#snippet footer()}
			<Button variant="ghost" size="sm" onclick={() => (rollbackDialogOpen = false)}>
				取消
			</Button>
			<Button
				variant="danger"
				size="sm"
				loading={rollingBack}
				onclick={() => void confirmRollback()}
			>
				<RotateCcw class="size-3.5" />
				<span>确认回滚</span>
			</Button>
		{/snippet}
	</Dialog>
</div>
