<script lang="ts">
	import {
		Ban,
		Flame,
		ImagePlus,
		RefreshCw,
		Save,
		Search,
		SmilePlus,
		Sparkles,
		Tag,
		Trash2,
		Upload,
		X
	} from '@lucide/svelte';
	import { api, type MemeItem } from '$lib/api';
	import {
		Badge,
		Button,
		Dialog,
		EmptyState,
		Input,
		PageHeader,
		SurfaceCard,
		Textarea,
		cn,
		type BadgeTone
	} from '$lib/components/ui';

	interface MemeCardItem extends MemeItem {
		overlay_text?: string;
	}

	const CURATED_EMOTIONS = ['开心', '无语', '嘲讽', '震惊', '委屈', '吃瓜', '点赞', '问号'];

	function splitTags(input: string | string[] | undefined): string[] {
		if (!input) return [];
		const items = Array.isArray(input) ? input : [input];
		const seen = new Set<string>();
		const out: string[] = [];
		for (const raw of items) {
			for (const part of raw.split(/[\s,，、/]+/)) {
				const token = part.trim();
				if (!token || seen.has(token)) continue;
				seen.add(token);
				out.push(token);
			}
		}
		return out;
	}

	function normalizeMeme(
		raw: MemeCardItem & { emotions?: string; scenes?: string }
	): MemeCardItem {
		const hash = raw.hash || raw.id || '';
		const emotionTags =
			raw.emotion_tags && raw.emotion_tags.length > 0
				? splitTags(raw.emotion_tags)
				: splitTags(raw.emotions);
		const sceneTags =
			raw.scene_tags && raw.scene_tags.length > 0
				? splitTags(raw.scene_tags)
				: splitTags(raw.scenes);
		return {
			...raw,
			id: raw.id || hash,
			hash,
			thumb_url: raw.thumb_url || (hash ? `/api/memes/${encodeURIComponent(hash)}/thumb` : ''),
			image_url: raw.image_url || (hash ? `/api/memes/${encodeURIComponent(hash)}/image` : ''),
			emotion_tags: emotionTags,
			scene_tags: sceneTags,
			overlay_text: raw.overlay_text ?? '',
			description: raw.description ?? '',
			use_count: typeof raw.use_count === 'number' ? raw.use_count : 0,
			blocked: Boolean(raw.blocked)
		};
	}

	let memes = $state.raw<MemeCardItem[]>([]);
	let totalCount = $state(0);
	let loading = $state(false);

	let query = $state('');
	let selectedEmotion = $state<string>('');
	let showBlocked = $state(false);

	let thumbFallback = $state<Record<string, boolean>>({});
	let imageBroken = $state<Record<string, boolean>>({});

	let notice = $state<{ tone: BadgeTone; text: string } | null>(null);
	let noticeTimer: ReturnType<typeof setTimeout> | null = null;

	// Upload Dialog state
	let uploadOpen = $state(false);
	let uploadFile = $state<File | null>(null);
	let uploadPreviewUrl = $state<string | null>(null);
	let uploadDescription = $state('');
	let uploadOverlay = $state('');
	let uploadEmotions = $state('');
	let uploading = $state(false);

	// Detail/Edit Dialog state
	let detailOpen = $state(false);
	let activeMeme = $state<MemeCardItem | null>(null);
	let editOverlay = $state('');
	let editDescription = $state('');
	let editEmotions = $state('');
	let editScenes = $state('');
	let savingDetail = $state(false);

	// Delete/Blacklist Confirmation Dialog state
	let deleteConfirmOpen = $state(false);
	let deleting = $state(false);

	let availableEmotions = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const m of memes) {
			for (const tag of m.emotion_tags) {
				counts.set(tag, (counts.get(tag) ?? 0) + 1);
			}
		}
		if (counts.size === 0) return CURATED_EMOTIONS;
		return [...counts.entries()]
			.sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
			.slice(0, 12)
			.map(([tag]) => tag);
	});

	let filteredMemes = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const emo = selectedEmotion.trim().toLowerCase();
		return memes.filter((m) => {
			if (!showBlocked && m.blocked) return false;
			if (emo && !m.emotion_tags.some((t) => t.toLowerCase() === emo)) return false;
			if (!q) return true;
			const haystack = [
				m.hash,
				m.overlay_text ?? '',
				m.description,
				...m.emotion_tags,
				...m.scene_tags
			]
				.join(' ')
				.toLowerCase();
			return haystack.includes(q);
		});
	});

	let editEmotionPreview = $derived(splitTags(editEmotions));
	let editScenePreview = $derived(splitTags(editScenes));

	function flash(tone: BadgeTone, text: string): void {
		notice = { tone, text };
		if (noticeTimer !== null) clearTimeout(noticeTimer);
		noticeTimer = setTimeout(() => {
			notice = null;
			noticeTimer = null;
		}, 3400);
	}

	async function loadMemes(signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const res = await api.listMemes({ blocked: showBlocked ? undefined : false, limit: 200 }, { signal });
			const normalized = (res.items ?? []).map((item) => normalizeMeme(item as MemeCardItem));
			memes = normalized;
			totalCount = typeof res.total === 'number' ? res.total : normalized.length;
		} catch {
			// Standalone preview or offline kernel: render clean empty gallery state.
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void loadMemes(ctrl.signal);
		return () => {
			ctrl.abort();
			if (noticeTimer !== null) clearTimeout(noticeTimer);
		};
	});

	$effect(() => {
		const file = uploadFile;
		if (!file) {
			uploadPreviewUrl = null;
			return;
		}
		const url = URL.createObjectURL(file);
		uploadPreviewUrl = url;
		return () => {
			URL.revokeObjectURL(url);
		};
	});

	function shortHash(hash: string): string {
		return hash ? `#${hash.slice(0, 6)}` : '#------';
	}

	function cardSrc(m: MemeCardItem): string {
		if (thumbFallback[m.id]) return m.image_url;
		return m.thumb_url || m.image_url;
	}

	function handleThumbError(m: MemeCardItem): void {
		if (!thumbFallback[m.id] && m.image_url && m.image_url !== m.thumb_url) {
			thumbFallback = { ...thumbFallback, [m.id]: true };
		} else {
			imageBroken = { ...imageBroken, [m.id]: true };
		}
	}

	function openUploadDialog(): void {
		uploadFile = null;
		uploadDescription = '';
		uploadOverlay = '';
		uploadEmotions = '';
		uploadOpen = true;
	}

	function handleFilePick(e: Event): void {
		const input = e.currentTarget as HTMLInputElement;
		const picked = input.files?.[0] ?? null;
		if (picked) {
			uploadFile = picked;
		}
	}

	async function submitUpload(): Promise<void> {
		if (!uploadFile) return;
		uploading = true;
		try {
			const form = new FormData();
			form.append('file', uploadFile);
			if (uploadDescription.trim()) form.append('description', uploadDescription.trim());
			if (uploadOverlay.trim()) form.append('overlay_text', uploadOverlay.trim());
			const emotions = splitTags(uploadEmotions);
			if (emotions.length > 0) form.append('emotion_tags', emotions.join(','));

			const res = await fetch('/api/memes', { method: 'POST', body: form });
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			uploadOpen = false;
			uploadFile = null;
			flash('mint', '表情已收录入库');
			await loadMemes();
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '上传表情失败');
		} finally {
			uploading = false;
		}
	}

	function openDetail(meme: MemeCardItem): void {
		activeMeme = meme;
		editOverlay = meme.overlay_text ?? '';
		editDescription = meme.description ?? '';
		editEmotions = meme.emotion_tags.join(', ');
		editScenes = meme.scene_tags.join(', ');
		detailOpen = true;
	}

	async function saveDetail(): Promise<void> {
		if (!activeMeme) return;
		savingDetail = true;
		const target = activeMeme;
		const nextEmotionTags = splitTags(editEmotions);
		const nextSceneTags = splitTags(editScenes);
		const patch: Partial<Pick<MemeItem, 'description' | 'emotion_tags' | 'scene_tags' | 'blocked'>> & {
			overlay_text?: string;
		} = {
			description: editDescription.trim(),
			overlay_text: editOverlay.trim(),
			emotion_tags: nextEmotionTags,
			scene_tags: nextSceneTags
		};
		try {
			const updated = normalizeMeme(
				(await api.updateMeme(target.id || target.hash, patch)) as MemeCardItem
			);
			memes = memes.map((m) => (m.id === target.id ? { ...updated, overlay_text: editOverlay.trim() } : m));
			activeMeme = { ...updated, overlay_text: editOverlay.trim() };
			detailOpen = false;
			flash('mint', `已更新 ${shortHash(target.hash)}`);
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '保存表情失败');
		} finally {
			savingDetail = false;
		}
	}

	async function toggleBlacklistActive(): Promise<void> {
		if (!activeMeme) return;
		deleting = true;
		const target = activeMeme;
		try {
			const nextBlocked = !target.blocked;
			await api.updateMeme(target.id || target.hash, { blocked: nextBlocked });
			memes = memes.map((m) => (m.id === target.id ? { ...m, blocked: nextBlocked } : m));
			activeMeme = { ...target, blocked: nextBlocked };
			deleteConfirmOpen = false;
			detailOpen = false;
			flash('amber', nextBlocked ? '已加入拉黑名单' : '已解除拉黑');
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '更新拉黑状态失败');
		} finally {
			deleting = false;
		}
	}

	async function confirmDeleteMeme(): Promise<void> {
		if (!activeMeme) return;
		deleting = true;
		const target = activeMeme;
		try {
			await api.deleteMeme(target.id || target.hash);
			memes = memes.filter((m) => m.id !== target.id);
			totalCount = Math.max(0, totalCount - 1);
			deleteConfirmOpen = false;
			detailOpen = false;
			activeMeme = null;
			flash('mint', `已拉黑并删除 ${shortHash(target.hash)}`);
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '删除表情失败');
		} finally {
			deleting = false;
		}
	}

	function formatSize(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="表情金库">
		{#snippet badge()}
			<div class="flex flex-wrap items-center gap-2">
				<Badge tone="amber" size="sm">{totalCount} 张</Badge>
				<Badge tone="neutral" size="sm">pHash 去重</Badge>
				{#if notice}
					<Badge tone={notice.tone} size="sm">{notice.text}</Badge>
				{/if}
			</div>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void loadMemes()}>
				<RefreshCw class="size-3.5" />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" onclick={openUploadDialog}>
				<Upload class="size-3.5" />
				<span>上传表情</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Instant Search & Emotion Filter Bar -->
	<SurfaceCard variant="raised" padding="md" class="space-y-3.5">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<Input
				bind:value={query}
				placeholder="即时搜索描述、配文、场景或 #hash..."
				class="sm:max-w-md"
			>
				{#snippet leading()}
					<Search class="size-4" />
				{/snippet}
				{#snippet trailing()}
					{#if query}
						<button
							type="button"
							aria-label="清空搜索"
							onclick={() => (query = '')}
							class="cursor-pointer rounded-full p-0.5 text-ink-muted hover:text-ink-primary"
						>
							<X class="size-3.5" />
						</button>
					{/if}
				{/snippet}
			</Input>

			<div class="flex flex-wrap items-center gap-2">
				<button
					type="button"
					onclick={() => {
						showBlocked = !showBlocked;
						void loadMemes();
					}}
					class={cn(
						'inline-flex cursor-pointer items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors',
						showBlocked
							? 'bg-tint-rose text-rose-600'
							: 'bg-surface-subtle text-ink-secondary hover:bg-surface-muted'
					)}
				>
					<Ban class="size-3.5" />
					<span>{showBlocked ? '包含已拉黑' : '隐藏已拉黑'}</span>
				</button>
				<Badge tone="neutral" size="sm">命中 {filteredMemes.length}</Badge>
			</div>
		</div>

		<!-- Emotion filter pills -->
		<div class="flex flex-wrap items-center gap-1.5">
			<span class="mr-1 inline-flex items-center gap-1 text-xs font-medium text-ink-muted">
				<Tag class="size-3.5" />
				<span>情绪</span>
			</span>
			<button
				type="button"
				onclick={() => (selectedEmotion = '')}
				class={cn(
					'cursor-pointer rounded-full px-3 py-1 text-xs font-medium transition-all duration-150',
					selectedEmotion === ''
						? 'bg-amber-500 text-white shadow-[0_6px_14px_-6px_rgba(242,162,27,0.7)]'
						: 'bg-surface-subtle text-ink-secondary hover:bg-tint-amber hover:text-amber-600'
				)}
			>
				全部
			</button>
			{#each availableEmotions as emo (emo)}
				{@const active = selectedEmotion === emo}
				<button
					type="button"
					onclick={() => (selectedEmotion = active ? '' : emo)}
					class={cn(
						'max-w-36 cursor-pointer truncate whitespace-nowrap rounded-full px-3 py-1 text-xs font-medium transition-all duration-150',
						active
							? 'bg-coral-500 text-white shadow-[0_6px_14px_-6px_rgba(255,115,84,0.65)]'
							: 'bg-surface-subtle text-ink-secondary hover:bg-tint-peach hover:text-coral-600'
					)}
				>
					{emo}
				</button>
			{/each}
		</div>
	</SurfaceCard>

	<!-- Responsive Playful Meme Grid -->
	{#if filteredMemes.length === 0}
		<EmptyState
			title={memes.length === 0 ? '表情金库暂无条目' : '没有匹配的表情'}
			hint={memes.length === 0 ? '上传新表情或等待群聊自动收录' : '试试切换情绪标签或清空搜索词'}
			tone="amber"
		>
			{#snippet icon()}
				<SmilePlus class="size-5" />
			{/snippet}
			{#snippet action()}
				{#if memes.length === 0}
					<Button variant="primary" size="sm" onclick={openUploadDialog}>
						<Upload class="size-3.5" />
						<span>上传首张表情</span>
					</Button>
				{:else}
					<Button
						variant="tonal"
						size="sm"
						onclick={() => {
							query = '';
							selectedEmotion = '';
						}}
					>
						重置筛选
					</Button>
				{/if}
			{/snippet}
		</EmptyState>
	{:else}
		<div class="grid grid-cols-[repeat(auto-fill,minmax(196px,1fr))] gap-4">
			{#each filteredMemes as meme (meme.id)}
				<button
					type="button"
					onclick={() => openDetail(meme)}
					class={cn(
						'group flex cursor-pointer flex-col gap-2.5 rounded-[var(--radius-card)] bg-surface-raised p-2.5 text-left shadow-[var(--shadow-card)]',
						'transition-all duration-200 hover:-translate-y-1 hover:shadow-[var(--shadow-float)]',
						'focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-mint-500/35',
						meme.blocked && 'opacity-65'
					)}
				>
					<div class="relative aspect-square w-full overflow-hidden rounded-[var(--radius-panel)] bg-surface-subtle">
						{#if !imageBroken[meme.id] && cardSrc(meme)}
							<img
								src={cardSrc(meme)}
								alt={meme.description || meme.overlay_text || shortHash(meme.hash)}
								loading="lazy"
								onerror={() => handleThumbError(meme)}
								class="size-full object-contain p-1.5 transition-transform duration-300 group-hover:scale-105"
							/>
						{:else}
							<div class="flex size-full flex-col items-center justify-center gap-1.5 text-ink-muted">
								<SmilePlus class="size-7 opacity-60" />
								<span class="font-mono text-[10px]">{shortHash(meme.hash)}</span>
							</div>
						{/if}

						<span
							class="absolute top-2 left-2 rounded-full bg-surface-raised/90 px-2 py-0.5 font-mono text-[10px] font-semibold text-ink-secondary shadow-[var(--shadow-xs)] backdrop-blur-sm"
						>
							{shortHash(meme.hash)}
						</span>

						<span
							class="absolute top-2 right-2 inline-flex items-center gap-0.5 rounded-full bg-tint-amber/95 px-2 py-0.5 font-mono text-[10px] font-bold text-amber-600 shadow-[var(--shadow-xs)] backdrop-blur-sm"
						>
							<Flame class="size-3" />
							<span>{meme.use_count}</span>
						</span>

						{#if meme.blocked}
							<span
								class="absolute right-2 bottom-2 rounded-full bg-rose-500 px-2 py-0.5 text-[10px] font-semibold text-white"
							>
								已拉黑
							</span>
						{/if}
					</div>

					<div class="flex flex-1 flex-col justify-between gap-2 px-1 pb-0.5">
						<p class="line-clamp-2 text-xs leading-snug font-medium text-ink-primary">
							{meme.overlay_text || meme.description || '无描述表情'}
						</p>

						<div class="flex flex-wrap gap-1">
							{#each meme.emotion_tags.slice(0, 3) as emo (emo)}
								<span
									class="max-w-full truncate whitespace-nowrap rounded-full bg-tint-peach px-2 py-0.5 text-[10px] font-medium text-coral-600"
								>
									{emo}
								</span>
							{/each}
							{#each meme.scene_tags.slice(0, 2) as scene (scene)}
								<span
									class="max-w-full truncate whitespace-nowrap rounded-full bg-tint-sky px-2 py-0.5 text-[10px] font-medium text-sky-600"
								>
									{scene}
								</span>
							{/each}
						</div>
					</div>
				</button>
			{/each}
		</div>
	{/if}

	<!-- Upload Meme Dialog -->
	<Dialog bind:open={uploadOpen} title="上传表情" size="md">
		<div class="space-y-4">
			<label
				class="group flex cursor-pointer flex-col items-center justify-center gap-3 rounded-[var(--radius-panel)] bg-surface-subtle p-5 text-center transition-colors hover:bg-tint-amber/60"
			>
				<input
					type="file"
					accept="image/*"
					onchange={handleFilePick}
					class="sr-only"
				/>
				{#if uploadPreviewUrl && uploadFile}
					<img
						src={uploadPreviewUrl}
						alt="上传预览"
						class="max-h-44 rounded-[var(--radius-control)] object-contain shadow-[var(--shadow-xs)]"
					/>
					<div class="flex flex-wrap items-center justify-center gap-2">
						<Badge tone="amber" size="sm">{uploadFile.name}</Badge>
						<Badge tone="neutral" size="sm">{formatSize(uploadFile.size)}</Badge>
					</div>
				{:else}
					<span class="flex size-12 items-center justify-center rounded-2xl bg-tint-amber text-amber-600 transition-transform group-hover:scale-105">
						<ImagePlus class="size-6" />
					</span>
					<div class="space-y-0.5">
						<div class="text-sm font-semibold text-ink-primary">选择表情图片 / GIF</div>
						<div class="text-xs text-ink-muted">PNG · JPG · WEBP · GIF，自动计算 pHash</div>
					</div>
				{/if}
			</label>

			<div class="space-y-3">
				<label class="block space-y-1.5">
					<span class="text-xs font-semibold text-ink-secondary">描述 / 印象（可选）</span>
					<Textarea
						bind:value={uploadDescription}
						rows={2}
						placeholder="留空则由视觉模型自动打标..."
					/>
				</label>

				<div class="grid gap-3 sm:grid-cols-2">
					<label class="block space-y-1.5">
						<span class="text-xs font-semibold text-ink-secondary">画面配文（可选）</span>
						<Input bind:value={uploadOverlay} placeholder="例如：真的假的" />
					</label>
					<label class="block space-y-1.5">
						<span class="text-xs font-semibold text-ink-secondary">情绪标签（逗号分隔）</span>
						<Input bind:value={uploadEmotions} placeholder="开心, 吃瓜" />
					</label>
				</div>
			</div>
		</div>

		{#snippet footer()}
			<Button variant="ghost" size="sm" onclick={() => (uploadOpen = false)}>取消</Button>
			<Button
				variant="primary"
				size="sm"
				disabled={!uploadFile}
				loading={uploading}
				onclick={() => void submitUpload()}
			>
				<Sparkles class="size-3.5" />
				<span>收录入库</span>
			</Button>
		{/snippet}
	</Dialog>

	<!-- Detail / Edit Meme Dialog -->
	<Dialog bind:open={detailOpen} title={activeMeme ? `表情详情 ${shortHash(activeMeme.hash)}` : '表情详情'} size="lg">
		{#if activeMeme}
			<div class="grid gap-5 sm:grid-cols-12">
				<!-- Preview column -->
				<div class="flex flex-col gap-3 sm:col-span-5">
					<div class="flex min-h-[210px] items-center justify-center overflow-hidden rounded-[var(--radius-panel)] bg-surface-subtle p-3">
						{#if activeMeme.image_url && !imageBroken[activeMeme.id]}
							<img
								src={activeMeme.image_url}
								alt={activeMeme.description || shortHash(activeMeme.hash)}
								class="max-h-60 w-full object-contain"
							/>
						{:else}
							<SmilePlus class="size-10 text-ink-muted opacity-50" />
						{/if}
					</div>

					<div class="flex flex-wrap items-center gap-1.5 rounded-[var(--radius-panel)] bg-surface-subtle p-2.5">
						<Badge tone="neutral" size="sm">{shortHash(activeMeme.hash)}</Badge>
						<Badge tone="amber" size="sm">使用 {activeMeme.use_count} 次</Badge>
						{#if activeMeme.blocked}
							<Badge tone="rose" size="sm">已拉黑</Badge>
						{/if}
					</div>
				</div>

				<!-- Edit form column -->
				<div class="space-y-3 sm:col-span-7">
					<label class="block space-y-1">
						<span class="text-xs font-semibold text-ink-secondary">画面配文 (Overlay Text)</span>
						<Input bind:value={editOverlay} placeholder="图片上的文字..." />
					</label>

					<label class="block space-y-1">
						<span class="text-xs font-semibold text-ink-secondary">视觉描述</span>
						<Textarea bind:value={editDescription} rows={3} placeholder="Bot 眼中的画面与梗点..." />
					</label>

					<label class="block space-y-1">
						<span class="text-xs font-semibold text-ink-secondary">情绪标签（逗号分隔）</span>
						<Input bind:value={editEmotions} placeholder="开心, 调侃, 无语" />
						{#if editEmotionPreview.length > 0}
							<div class="flex flex-wrap gap-1 pt-1">
								{#each editEmotionPreview as tag (tag)}
									<Badge tone="coral" size="sm">{tag}</Badge>
								{/each}
							</div>
						{/if}
					</label>

					<label class="block space-y-1">
						<span class="text-xs font-semibold text-ink-secondary">适用场景（逗号分隔）</span>
						<Input bind:value={editScenes} placeholder="接梗, 群友翻车, 夸人" />
						{#if editScenePreview.length > 0}
							<div class="flex flex-wrap gap-1 pt-1">
								{#each editScenePreview as tag (tag)}
									<Badge tone="sky" size="sm">{tag}</Badge>
								{/each}
							</div>
						{/if}
					</label>
				</div>
			</div>
		{/if}

		{#snippet footer()}
			<div class="flex w-full flex-wrap items-center justify-between gap-2">
				<Button variant="danger" size="sm" onclick={() => (deleteConfirmOpen = true)}>
					<Trash2 class="size-3.5" />
					<span>拉黑 / 删除</span>
				</Button>
				<div class="flex items-center gap-2">
					<Button variant="ghost" size="sm" onclick={() => (detailOpen = false)}>取消</Button>
					<Button
						variant="primary"
						size="sm"
						loading={savingDetail}
						onclick={() => void saveDetail()}
					>
						<Save class="size-3.5" />
						<span>保存修改</span>
					</Button>
				</div>
			</div>
		{/snippet}
	</Dialog>

	<!-- Blacklist / Delete Confirmation Dialog -->
	<Dialog bind:open={deleteConfirmOpen} title="确认拉黑或删除" size="sm">
		{#if activeMeme}
			<div class="space-y-3">
				<div class="flex items-center justify-between gap-2 rounded-[var(--radius-panel)] bg-tint-rose p-3">
					<span class="font-mono text-xs font-bold text-rose-600">{shortHash(activeMeme.hash)}</span>
					<span class="truncate text-xs text-ink-primary">
						{activeMeme.overlay_text || activeMeme.description || '未命名表情'}
					</span>
				</div>
				<p class="text-xs leading-relaxed text-ink-secondary">
					拉黑并删除将记录该图片哈希至黑名单，防止群聊再次自动收录。
				</p>
			</div>
		{/if}

		{#snippet footer()}
			<Button variant="ghost" size="sm" onclick={() => (deleteConfirmOpen = false)}>取消</Button>
			<Button
				variant="tonal"
				size="sm"
				loading={deleting}
				onclick={() => void toggleBlacklistActive()}
			>
				<Ban class="size-3.5" />
				<span>{activeMeme?.blocked ? '解除拉黑' : '仅拉黑'}</span>
			</Button>
			<Button
				variant="danger"
				size="sm"
				loading={deleting}
				onclick={() => void confirmDeleteMeme()}
			>
				<Trash2 class="size-3.5" />
				<span>彻底删除</span>
			</Button>
		{/snippet}
	</Dialog>
</div>
