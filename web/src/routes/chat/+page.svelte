<script lang="ts">
	import {
		AtSign,
		Bot,
		Hand,
		Hash,
		Image as ImageIcon,
		Link2,
		MessageCircleHeart,
		Mic,
		RefreshCw,
		Reply,
		Send,
		Smile,
		Sparkles,
		X,
		Zap
	} from '@lucide/svelte';
	import { api, type ContentBlock, type TimelineMessage } from '$lib/api';
	import {
		Badge,
		Button,
		EmptyState,
		Input,
		PageHeader,
		SurfaceCard,
		Switch,
		cn
	} from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	interface ParsedBotSegment {
		kind: 'text' | 'meme' | 'voice' | 'react' | 'poke';
		text?: string;
		ref?: string;
		emoji?: string;
		target?: string;
	}

	const PRESET_ROOMS = ['debug-room', 'group-main'];

	const SAMPLE_PROMPTS = [
		{ label: '连胜好心情', text: '今天排位五连胜太爽了！' },
		{ label: '回忆测试', text: '你还记得我之前说过最喜欢吃什么吗？' },
		{ label: '表情包互动', text: '快发个表情包助助兴' },
		{ label: '低电量摸鱼', text: '下午有点困，好想摸鱼发呆...' }
	];

	let selectedRoom = $state('debug-room');
	let discoveredRooms = $state.raw<string[]>([]);
	let customRoomMode = $state(false);
	let customRoomInput = $state('');

	let draft = $state('');
	let atBot = $state(true);
	let bypassGate = $state(true);
	let replyToMsgId = $state<string | null>(null);

	let history = $state.raw<TimelineMessage[]>([]);
	let optimistic = $state.raw<TimelineMessage[]>([]);
	let loading = $state(false);
	let sending = $state(false);
	let sendNotice = $state<{ tone: 'mint' | 'amber' | 'rose'; text: string } | null>(null);
	let failedThumbs = $state<Record<string, boolean>>({});

	let scrollBox: HTMLDivElement | undefined = $state();

	let availableRooms = $derived.by(() => {
		const set = new Set<string>(PRESET_ROOMS);
		for (const room of discoveredRooms) {
			if (room) set.add(room);
		}
		set.add(selectedRoom);
		for (const msg of realtime.liveTimeline) {
			if (msg.room_id) set.add(msg.room_id);
		}
		if (realtime.activeRun?.room_id) {
			set.add(realtime.activeRun.room_id);
		}
		return Array.from(set);
	});

	let messages = $derived.by<TimelineMessage[]>(() => {
		const byId = new Map<string, TimelineMessage>();
		for (const msg of history) {
			byId.set(msg.msg_id, msg);
		}
		for (const msg of realtime.liveTimeline) {
			if (msg.room_id === selectedRoom) {
				byId.set(msg.msg_id, msg);
			}
		}
		for (const msg of optimistic) {
			if (msg.room_id !== selectedRoom) continue;
			const echoed = Array.from(byId.values()).some(
				(m) =>
					!m.is_bot &&
					Math.abs(m.timestamp_ms - msg.timestamp_ms) < 5000 &&
					extractPlainText(m) === extractPlainText(msg)
			);
			if (!echoed) {
				byId.set(msg.msg_id, msg);
			}
		}
		return Array.from(byId.values()).sort((a, b) => a.timestamp_ms - b.timestamp_ms);
	});

	let messageById = $derived.by(() => {
		const map = new Map<string, TimelineMessage>();
		for (const msg of messages) {
			map.set(msg.msg_id, msg);
		}
		return map;
	});

	let replyTarget = $derived(replyToMsgId ? messageById.get(replyToMsgId) ?? null : null);

	let roomRunActive = $derived(
		Boolean(
			realtime.activeRun &&
				(!realtime.activeRun.room_id || realtime.activeRun.room_id === selectedRoom) &&
				realtime.activeRun.stage !== 'idle' &&
				realtime.activeRun.stage !== 'done' &&
				realtime.activeRun.stage !== 'error'
		)
	);

	async function loadTimeline(roomId: string, signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const res = await api.getTimeline({ room_id: roomId, limit: 80 }, { signal });
			history = res.items ?? [];
		} catch {
			// Standalone offline preview keeps local timeline clean.
			history = [];
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void api
			.getDashboard({ signal: ctrl.signal })
			.then((res) => {
				if (res.rooms && res.rooms.length > 0) {
					discoveredRooms = res.rooms;
				}
			})
			.catch(() => {
				// Standalone offline preview keeps preset rooms.
			});
		return () => ctrl.abort();
	});

	$effect(() => {
		const room = selectedRoom;
		const ctrl = new AbortController();
		void loadTimeline(room, ctrl.signal);
		return () => ctrl.abort();
	});

	$effect(() => {
		// Re-run whenever message count or active run indicator changes to keep bottom pinned.
		const _len = messages.length;
		const _active = roomRunActive;
		void _len;
		void _active;
		if (scrollBox) {
			scrollBox.scrollTop = scrollBox.scrollHeight;
		}
	});

	$effect(() => {
		if (!sendNotice) return;
		const timer = setTimeout(() => {
			sendNotice = null;
		}, 4000);
		return () => clearTimeout(timer);
	});

	async function sendMessage(rawText?: string): Promise<void> {
		const base = (rawText ?? draft).trim();
		if (!base || sending) return;

		const composed = atBot && !base.startsWith('@') ? `@omni-bot ${base}` : base;
		const replyId = replyToMsgId ?? undefined;
		const now = Date.now();
		const localId = `local-${now}`;

		const blocks: ContentBlock[] = [];
		if (atBot) {
			blocks.push({ type: 'mention', target_id: 'bot', target_name: 'omni-bot' });
		}
		blocks.push({ type: 'text', text: base });

		const localMsg: TimelineMessage = {
			msg_id: localId,
			channel_id: 'webui',
			room_id: selectedRoom,
			sender_id: 'webui-user',
			sender_name: '调试员',
			is_bot: false,
			timestamp_ms: now,
			blocks,
			reply_to_msg_id: replyId
		};

		optimistic = [...optimistic, localMsg];
		if (!rawText) draft = '';
		replyToMsgId = null;
		sending = true;
		sendNotice = null;

		try {
			await api.sendChat({
				channel_id: 'webui',
				room_id: selectedRoom,
				text: composed,
				...(replyId ? { reply_to_msg_id: replyId } : {})
			});

			if (bypassGate) {
				await api.triggerRun({
					room_id: selectedRoom,
					source: 'webui_debug',
					bypass_gate: true
				});
			}

			sendNotice = {
				tone: 'mint',
				text: bypassGate ? '已发送并跳过 Gate 触发' : '已发送至调试房间'
			};
		} catch {
			sendNotice = {
				tone: 'amber',
				text: 'Kernel 未连接，已暂存于本地调试画布'
			};
		} finally {
			sending = false;
		}
	}

	function extractPlainText(msg: TimelineMessage): string {
		return msg.blocks
			.map((b) => b.text ?? b.title ?? '')
			.filter(Boolean)
			.join(' ')
			.trim();
	}

	function parseInlineProtocol(raw: string): ParsedBotSegment[] {
		const bubbles = raw
			.split('|||')
			.map((part) => part.trim())
			.filter(Boolean);
		const out: ParsedBotSegment[] = [];
		const tokenRe = /\[(meme:#?[^\]]+|voice:[^\]]+|react:[^\]]+|poke:[^\]]+|reply:[^\]]+)\]/g;

		for (const bubble of bubbles) {
			let lastIdx = 0;
			let textBuffer = '';

			for (const match of bubble.matchAll(tokenRe)) {
				const idx = match.index ?? 0;
				textBuffer += bubble.slice(lastIdx, idx);
				lastIdx = idx + match[0].length;
				const inner = match[1];

				if (inner.startsWith('reply:')) {
					continue;
				}
				if (textBuffer.trim()) {
					out.push({ kind: 'text', text: textBuffer.trim() });
					textBuffer = '';
				}
				if (inner.startsWith('meme:')) {
					const ref = inner.slice(5).replace(/^#/, '').trim();
					out.push({ kind: 'meme', ref });
				} else if (inner.startsWith('voice:')) {
					out.push({ kind: 'voice', text: inner.slice(6).trim() });
				} else if (inner.startsWith('react:')) {
					const parts = inner.slice(6).split(':');
					out.push({
						kind: 'react',
						target: parts[0]?.trim(),
						emoji: parts.slice(1).join(':').trim() || '✨'
					});
				} else if (inner.startsWith('poke:')) {
					out.push({ kind: 'poke', target: inner.slice(5).trim() });
				}
			}

			textBuffer += bubble.slice(lastIdx);
			if (textBuffer.trim()) {
				out.push({ kind: 'text', text: textBuffer.trim() });
			}
		}

		return out;
	}

	function findReplyRefId(msg: TimelineMessage): string | undefined {
		if (msg.reply_to_msg_id) return msg.reply_to_msg_id;
		const refBlock = msg.blocks.find((b) => b.type === 'reply_ref' && b.reply_to_msg_id);
		if (refBlock?.reply_to_msg_id) return refBlock.reply_to_msg_id;
		for (const b of msg.blocks) {
			if (b.type === 'text' && b.text) {
				const m = b.text.match(/\[reply:([^\]]+)\]/);
				if (m?.[1]) return m[1].trim();
			}
		}
		return undefined;
	}

	function formatTime(ms: number): string {
		if (!ms) return '';
		return new Date(ms).toLocaleTimeString('zh-CN', {
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit',
			hour12: false
		});
	}

	function applyCustomRoom(): void {
		const trimmed = customRoomInput.trim();
		if (trimmed) {
			selectedRoom = trimmed;
			optimistic = [];
		}
		customRoomInput = '';
		customRoomMode = false;
	}

	function markThumbFailed(key: string): void {
		failedThumbs = { ...failedThumbs, [key]: true };
	}
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4">
	<PageHeader title="对话调试" class="pb-1">
		{#snippet badge()}
			<Badge
				tone={realtime.connectionStatus === 'connected' ? 'mint' : 'neutral'}
				size="sm"
				dot
				pulse={realtime.connectionStatus === 'connected'}
			>
				{realtime.connectionStatus === 'connected' ? 'WS 实时' : 'WS 离线'}
			</Badge>
			<Badge tone={realtime.moodTone} size="sm">{realtime.moodLabel}</Badge>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void loadTimeline(selectedRoom)}>
				<RefreshCw class={cn('size-3.5', loading && 'animate-spin')} />
				<span>同步时间线</span>
			</Button>
		{/snippet}
	</PageHeader>

	<!-- Messaging Canvas -->
	<SurfaceCard variant="raised" padding="none" class="flex flex-col overflow-hidden">
		<!-- Compact Room Header Strip -->
		<div class="flex flex-wrap items-center justify-between gap-2 bg-surface-subtle/75 px-4 py-2.5">
			<div class="flex flex-wrap items-center gap-1.5">
				<span class="inline-flex items-center gap-1 text-xs font-semibold text-ink-secondary">
					<Hash class="size-3.5 text-mint-600" />
					房间
				</span>
				{#each availableRooms as room (room)}
					{@const active = selectedRoom === room}
					<button
						type="button"
						onclick={() => {
							selectedRoom = room;
							replyToMsgId = null;
						}}
						class={cn(
							'cursor-pointer rounded-[var(--radius-pill)] px-2.5 py-1 font-mono text-xs font-medium transition-all',
							active
								? 'bg-surface-raised text-ink-primary shadow-[var(--shadow-xs)]'
								: 'text-ink-secondary hover:bg-surface-raised/60 hover:text-ink-primary'
						)}
					>
						{room}
					</button>
				{/each}

				{#if customRoomMode}
					<form
						class="inline-flex items-center gap-1"
						onsubmit={(e) => {
							e.preventDefault();
							applyCustomRoom();
						}}
					>
						<input
							bind:value={customRoomInput}
							placeholder="room-id"
							class="h-7 w-28 rounded-full bg-surface-raised px-2.5 font-mono text-xs text-ink-primary focus:ring-2 focus:ring-mint-500/35 focus:outline-none"
						/>
						<Button type="submit" variant="tonal" size="sm" class="h-7 px-2.5">切换</Button>
					</form>
				{:else}
					<button
						type="button"
						onclick={() => (customRoomMode = true)}
						class="cursor-pointer rounded-[var(--radius-pill)] bg-surface-raised/70 px-2.5 py-1 text-xs font-medium text-ink-muted hover:text-ink-primary"
					>
						+ 自定义
					</button>
				{/if}
			</div>

			{#if sendNotice}
				<Badge
					tone={sendNotice.tone === 'mint' ? 'mint' : sendNotice.tone === 'amber' ? 'amber' : 'rose'}
					size="sm"
					class="animate-pop-in"
				>
					{sendNotice.text}
				</Badge>
			{/if}
		</div>

		<!-- Scrollable Message Timeline -->
		<div
			bind:this={scrollBox}
			class="flex min-h-[380px] max-h-[56dvh] flex-col gap-3.5 overflow-y-auto bg-surface-canvas/55 p-4 sm:p-5"
		>
			{#if messages.length === 0}
				<div class="my-auto">
					<EmptyState
						title={`房间 ${selectedRoom} 暂无消息`}
						hint="点击下方快捷示例或输入文本即刻验证情绪、记忆与多气泡回复"
						tone="mint"
						class="bg-transparent py-10 shadow-none"
					>
						{#snippet icon()}
							<MessageCircleHeart class="size-5" />
						{/snippet}
					</EmptyState>
				</div>
			{:else}
				{#each messages as msg (msg.msg_id)}
					{@const replyId = findReplyRefId(msg)}
					{@const quoted = replyId ? messageById.get(replyId) : undefined}
					<div class={cn('group flex flex-col gap-1', msg.is_bot ? 'items-start' : 'items-end')}>
						<!-- Sender & Clock Row -->
						<div class="flex items-center gap-2 px-1 text-[11px] text-ink-muted">
							{#if msg.is_bot}
								<span class="inline-flex items-center gap-1 font-semibold text-mint-700">
									<Bot class="size-3.5" />
									{msg.sender_name || 'omni-bot'}
								</span>
							{:else}
								<span class="font-medium text-ink-secondary">{msg.sender_name || '调试员'}</span>
							{/if}
							<span class="font-mono">{formatTime(msg.timestamp_ms)}</span>
							<button
								type="button"
								title="引用回复"
								onclick={() => (replyToMsgId = msg.msg_id)}
								class="inline-flex cursor-pointer items-center gap-0.5 rounded-full px-1.5 py-0.5 text-[10px] text-ink-muted opacity-0 transition-opacity group-hover:opacity-100 hover:bg-surface-subtle hover:text-ink-primary focus-visible:opacity-100"
							>
								<Reply class="size-3" />
								<span>回复</span>
							</button>
						</div>

						<!-- Reply Quote Chip -->
						{#if replyId}
							<div class="inline-flex max-w-[85%] items-center gap-1.5 rounded-[var(--radius-pill)] bg-surface-subtle px-3 py-1 text-[11px] text-ink-secondary sm:max-w-[72%]">
								<Reply class="size-3 shrink-0 text-mint-600" />
								<span class="truncate">
									{#if quoted}
										{quoted.sender_name}: {extractPlainText(quoted) || quoted.msg_id}
									{:else}
										引用 #{replyId}
									{/if}
								</span>
							</div>
						{/if}

						<!-- Message Content Blocks & Multi-bubble Rendering -->
						<div
							class={cn(
								'flex max-w-[88%] flex-col gap-1.5 sm:max-w-[75%]',
								msg.is_bot ? 'items-start' : 'items-end'
							)}
						>
							{#each msg.blocks as block, bIdx (`${msg.msg_id}-b-${bIdx}`)}
								{#if block.type === 'mention'}
									<span class="inline-flex items-center gap-1 rounded-[var(--radius-pill)] bg-tint-sky px-2.5 py-0.5 text-[11px] font-semibold text-sky-600">
										<AtSign class="size-3" />
										{block.target_name || block.target_id || 'bot'}
									</span>
								{:else if block.type === 'text' && block.text}
									{#if msg.is_bot}
										{@const segments = parseInlineProtocol(block.text)}
										{#each segments as seg, sIdx (`${msg.msg_id}-s-${bIdx}-${sIdx}`)}
											{#if seg.kind === 'text'}
												<div class="rounded-[var(--radius-card)] rounded-tl-md bg-surface-raised px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap break-words text-ink-primary shadow-[var(--shadow-card)]">
													{seg.text}
												</div>
											{:else if seg.kind === 'meme'}
												{@const thumbKey = `${msg.msg_id}-${seg.ref}`}
												<div class="flex items-center gap-2.5 rounded-[var(--radius-panel)] bg-tint-amber p-2.5 shadow-[var(--shadow-xs)]">
													{#if seg.ref && !failedThumbs[thumbKey]}
														<img
															src={`/api/memes/${encodeURIComponent(seg.ref)}/thumb`}
															alt={seg.ref}
															onerror={() => markThumbFailed(thumbKey)}
															class="size-16 shrink-0 rounded-xl object-cover"
														/>
													{:else}
														<span class="flex size-10 shrink-0 items-center justify-center rounded-xl bg-surface-raised text-amber-600">
															<ImageIcon class="size-4" />
														</span>
													{/if}
													<div class="min-w-0 pr-1">
														<Badge tone="amber" size="sm">表情包</Badge>
														<div class="mt-1 truncate font-mono text-[11px] text-ink-secondary">
															#{seg.ref}
														</div>
													</div>
												</div>
											{:else if seg.kind === 'voice'}
												<div class="inline-flex items-center gap-2 rounded-[var(--radius-pill)] bg-tint-sky px-3.5 py-2 text-xs font-medium text-sky-600 shadow-[var(--shadow-xs)]">
													<Mic class="size-3.5 shrink-0" />
													<span>{seg.text || '语音回复'}</span>
												</div>
											{:else if seg.kind === 'react'}
												<div class="inline-flex items-center gap-1.5 rounded-[var(--radius-pill)] bg-tint-lilac px-3 py-1.5 text-xs font-medium text-lilac-600">
													<Smile class="size-3.5 shrink-0" />
													<span>{seg.emoji}</span>
													{#if seg.target}
														<span class="font-mono text-[10px] opacity-75">#{seg.target}</span>
													{/if}
												</div>
											{:else if seg.kind === 'poke'}
												<div class="inline-flex items-center gap-1.5 rounded-[var(--radius-pill)] bg-tint-peach px-3 py-1.5 text-xs font-medium text-coral-600">
													<Hand class="size-3.5 shrink-0" />
													<span>戳了戳 {seg.target || '你'}</span>
												</div>
											{/if}
										{/each}
									{:else}
										<div class="rounded-[var(--radius-card)] rounded-tr-md bg-mint-500 px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap break-words text-white shadow-[0_8px_20px_-10px_rgba(24,184,136,0.75)]">
											{block.text}
										</div>
									{/if}
								{:else if block.type === 'image' && block.media?.blob_id}
									<div class="overflow-hidden rounded-[var(--radius-panel)] bg-surface-raised p-1.5 shadow-[var(--shadow-card)]">
										<img
											src={api.blobUrl(block.media.blob_id)}
											alt={block.title || 'image'}
											class="max-h-48 rounded-[calc(var(--radius-panel)-4px)] object-cover"
										/>
									</div>
								{:else if block.type === 'audio'}
									<div class="inline-flex items-center gap-2 rounded-[var(--radius-pill)] bg-tint-sky px-3.5 py-2 text-xs font-medium text-sky-600 shadow-[var(--shadow-xs)]">
										<Mic class="size-3.5 shrink-0" />
										<span>{block.text || '语音消息'}</span>
										{#if block.media?.duration_ms}
											<span class="font-mono text-[11px]">
												{(block.media.duration_ms / 1000).toFixed(1)}s
											</span>
										{/if}
									</div>
								{:else if block.type === 'link'}
									<a
										href={block.url || '#'}
										target="_blank"
										rel="noreferrer"
										class="inline-flex items-center gap-1.5 rounded-[var(--radius-panel)] bg-surface-raised px-3 py-2 text-xs font-medium text-sky-600 shadow-[var(--shadow-xs)] hover:bg-tint-sky"
									>
										<Link2 class="size-3.5 shrink-0" />
										<span class="truncate">{block.title || block.url}</span>
									</a>
								{/if}
							{/each}
						</div>
					</div>
				{/each}
			{/if}

			{#if roomRunActive && realtime.activeRun}
				<div class="animate-pop-in flex items-center gap-2 self-start rounded-[var(--radius-pill)] bg-tint-mint px-3.5 py-2 text-xs font-medium text-mint-700 shadow-[var(--shadow-xs)]">
					<span class="relative inline-flex size-2">
						<span class="absolute inline-flex size-full animate-ping rounded-full bg-mint-500 opacity-75"></span>
						<span class="relative inline-flex size-2 rounded-full bg-mint-500"></span>
					</span>
					<span>思考中 · {realtime.activeRun.stage}</span>
					<a
						href={`/traces?run=${encodeURIComponent(realtime.activeRun.run_id)}`}
						class="ml-1 font-mono text-[11px] underline underline-offset-2"
					>
						轨迹
					</a>
				</div>
			{/if}
		</div>

		<!-- Compact Bottom Composer -->
		<div class="space-y-2.5 bg-surface-raised p-3.5 sm:p-4">
			<!-- Quick Sample Prompt Pills -->
			<div class="flex flex-wrap items-center gap-1.5">
				{#each SAMPLE_PROMPTS as sample (sample.label)}
					<button
						type="button"
						disabled={sending}
						onclick={() => void sendMessage(sample.text)}
						class="inline-flex cursor-pointer items-center gap-1 rounded-[var(--radius-pill)] bg-surface-subtle px-3 py-1 text-xs font-medium text-ink-secondary transition-colors hover:bg-tint-mint hover:text-mint-700 disabled:pointer-events-none disabled:opacity-50"
					>
						<Sparkles class="size-3 text-mint-500" />
						<span>{sample.label}</span>
					</button>
				{/each}
			</div>

			{#if replyToMsgId}
				<div class="flex items-center justify-between gap-2 rounded-[var(--radius-control)] bg-tint-mint px-3 py-1.5 text-xs text-mint-700">
					<span class="inline-flex min-w-0 items-center gap-1.5">
						<Reply class="size-3.5 shrink-0" />
						<span class="truncate">
							回复 {replyTarget?.sender_name ?? ''}: {replyTarget ? extractPlainText(replyTarget) : replyToMsgId}
						</span>
					</span>
					<button
						type="button"
						aria-label="取消引用"
						onclick={() => (replyToMsgId = null)}
						class="cursor-pointer rounded-full p-0.5 hover:bg-black/5"
					>
						<X class="size-3.5" />
					</button>
				</div>
			{/if}

			<!-- Debug Switches + Input Form -->
			<div class="flex flex-wrap items-center justify-between gap-3">
				<div class="flex flex-wrap items-center gap-4">
					<label class="inline-flex cursor-pointer items-center gap-2 text-xs font-medium text-ink-secondary select-none">
						<Switch bind:checked={atBot} tone="mint" label="@bot 提及" />
						<span class="inline-flex items-center gap-1">
							<AtSign class="size-3.5 text-mint-600" />
							@bot
						</span>
					</label>

					<label class="inline-flex cursor-pointer items-center gap-2 text-xs font-medium text-ink-secondary select-none">
						<Switch bind:checked={bypassGate} tone="coral" label="Bypass Gate 调试直通" />
						<span class="inline-flex items-center gap-1">
							<Zap class="size-3.5 text-coral-500" />
							Bypass Gate
						</span>
					</label>
				</div>

				<span class="font-mono text-[11px] text-ink-muted">
					#{selectedRoom} · {messages.length} 条
				</span>
			</div>

			<form
				class="flex items-center gap-2"
				onsubmit={(e) => {
					e.preventDefault();
					void sendMessage();
				}}
			>
				<Input
					bind:value={draft}
					placeholder={`发送至 #${selectedRoom}...`}
					disabled={sending}
					class="flex-1"
				/>
				<Button type="submit" variant="primary" loading={sending} disabled={!draft.trim()}>
					<Send class="size-4" />
					<span>发送</span>
				</Button>
			</form>
		</div>
	</SurfaceCard>
</div>
