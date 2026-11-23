<script lang="ts">
	import {
		AudioLines,
		Brain,
		Cpu,
		FolderLock,
		HeartPulse,
		KeyRound,
		Plug,
		Plus,
		RefreshCw,
		RotateCw,
		Save,
		Server,
		ShieldCheck,
		SlidersHorizontal,
		SmilePlus,
		Terminal,
		Trash2
	} from '@lucide/svelte';
	import {
		api,
		type MCPServerConfig,
		type ModelSlotConfig,
		type ProviderConfig,
		type RuntimeConfig,
		type RuntimeConfigPatch
	} from '$lib/api';
	import {
		Badge,
		Button,
		Dialog,
		Input,
		PageHeader,
		SurfaceCard,
		Switch,
		Tabs,
		cn,
		type BadgeTone,
		type TabItem
	} from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	type SlotKey = 'chat' | 'decision' | 'summary' | 'vision' | 'embedding' | 'tts' | 'asr';
	type CapabilityKey = 'vision' | 'audio_in' | 'video_in' | 'tool_use' | 'thinking';

	interface EditableProvider extends ProviderConfig {
		secret_key: string;
	}

	interface SecretStatusItem {
		key: string;
		configured: boolean;
		masked_hint: string;
		updated_at_ms?: number;
	}

	interface EditableSlot {
		key: SlotKey;
		wireSlot: ModelSlotConfig['slot'];
		label: string;
		tone: BadgeTone;
		provider_id: string;
		model_id: string;
		capabilities: Record<CapabilityKey, boolean>;
	}

	interface EditableMCPServer extends MCPServerConfig {
		argsText: string;
	}

	interface EditableFeatures {
		memory_enabled: boolean;
		emotion_enabled: boolean;
		meme_collect_enabled: boolean;
		voice_enabled: boolean;
		bash_enabled: boolean;
	}

	interface EditableBehavior {
		bot_name: string;
		bot_aliases: string;
		gate_threshold: string;
		gate_at_threshold: string;
		debounce_ms: string;
	}

	const PROVIDER_KINDS = ['gemini', 'openai', 'fishaudio', 'whisper', 'custom'] as const;

	const CAPABILITY_ITEMS: { key: CapabilityKey; label: string }[] = [
		{ key: 'vision', label: 'Vision' },
		{ key: 'audio_in', label: 'AudioIn' },
		{ key: 'video_in', label: 'VideoIn' },
		{ key: 'tool_use', label: 'ToolUse' },
		{ key: 'thinking', label: 'Thinking' }
	];

	function createDefaultSlots(): EditableSlot[] {
		return [
			{
				key: 'chat',
				wireSlot: 'chat',
				label: '主对话 Chat',
				tone: 'mint',
				provider_id: '',
				model_id: '',
				capabilities: { vision: true, audio_in: true, video_in: true, tool_use: true, thinking: true }
			},
			{
				key: 'decision',
				wireSlot: 'decision',
				label: '门控评估 Decision',
				tone: 'coral',
				provider_id: '',
				model_id: '',
				capabilities: { vision: false, audio_in: false, video_in: false, tool_use: false, thinking: false }
			},
			{
				key: 'summary',
				wireSlot: 'summary',
				label: '滚动摘要 Summary',
				tone: 'sky',
				provider_id: '',
				model_id: '',
				capabilities: { vision: false, audio_in: false, video_in: false, tool_use: false, thinking: false }
			},
			{
				key: 'vision',
				wireSlot: 'vision',
				label: '视觉理解 Vision',
				tone: 'amber',
				provider_id: '',
				model_id: '',
				capabilities: { vision: true, audio_in: false, video_in: false, tool_use: false, thinking: false }
			},
			{
				key: 'embedding',
				wireSlot: 'embedding',
				label: '语义向量 Embedding',
				tone: 'lilac',
				provider_id: '',
				model_id: '',
				capabilities: { vision: false, audio_in: false, video_in: false, tool_use: false, thinking: false }
			},
			{
				key: 'tts',
				wireSlot: 'tts',
				label: '语音合成 TTS',
				tone: 'coral',
				provider_id: '',
				model_id: '',
				capabilities: { vision: false, audio_in: false, video_in: false, tool_use: false, thinking: false }
			},
			{
				key: 'asr',
				wireSlot: 'stt',
				label: '语音转写 ASR',
				tone: 'sky',
				provider_id: '',
				model_id: '',
				capabilities: { vision: false, audio_in: true, video_in: false, tool_use: false, thinking: false }
			}
		];
	}

	const DEFAULT_FEATURES: EditableFeatures = {
		memory_enabled: true,
		emotion_enabled: true,
		meme_collect_enabled: true,
		voice_enabled: false,
		bash_enabled: false
	};

	const DEFAULT_BEHAVIOR: EditableBehavior = {
		bot_name: 'bk101',
		bot_aliases: 'bk101|101',
		gate_threshold: '0.70',
		gate_at_threshold: '0.30',
		debounce_ms: '1400'
	};

	let tab = $state('providers');
	let loading = $state(false);
	let saving = $state(false);
	let reloadingMcpId = $state<string | null>(null);
	let savingSecretKey = $state<string | null>(null);
	let deletingSecretKey = $state<string | null>(null);

	let notice = $state<{ tone: BadgeTone; text: string } | null>(null);
	let noticeTimer: ReturnType<typeof setTimeout> | null = null;

	let providers = $state<EditableProvider[]>([]);
	let secrets = $state<SecretStatusItem[]>([]);
	let secretInputs = $state<Record<string, string>>({});
	let newSecretKey = $state('');
	let newSecretValue = $state('');

	let slots = $state<EditableSlot[]>(createDefaultSlots());
	let mcpServers = $state<EditableMCPServer[]>([]);
	let features = $state<EditableFeatures>({ ...DEFAULT_FEATURES });
	let behavior = $state<EditableBehavior>({ ...DEFAULT_BEHAVIOR });
	let sandboxWorkspace = $state('data/sandbox');
	let savedSnapshot = $state('');

	let deleteSecretConfirm = $state<string | null>(null);
	let deleteSecretDialogOpen = $state(false);

	let currentSnapshot = $derived(
		JSON.stringify({ providers, slots, mcpServers, features, behavior })
	);
	let isDirty = $derived(savedSnapshot !== '' && currentSnapshot !== savedSnapshot);

	let mergedSecrets = $derived.by<SecretStatusItem[]>(() => {
		const map = new Map<string, SecretStatusItem>();
		for (const s of secrets) {
			map.set(s.key, s);
		}
		for (const p of providers) {
			const key = p.secret_key.trim();
			if (key && !map.has(key)) {
				map.set(key, {
					key,
					configured: p.has_api_key,
					masked_hint: p.api_key_hint ?? ''
				});
			}
		}
		return [...map.values()].sort((a, b) => a.key.localeCompare(b.key));
	});

	let tabs = $derived<TabItem[]>([
		{ value: 'providers', label: '供应商与密钥', badge: providers.length },
		{ value: 'slots', label: '模型槽位', badge: slots.length },
		{ value: 'mcp', label: '行为与扩展', badge: mcpServers.length }
	]);

	function flash(tone: BadgeTone, text: string): void {
		notice = { tone, text };
		if (noticeTimer !== null) clearTimeout(noticeTimer);
		noticeTimer = setTimeout(() => {
			notice = null;
			noticeTimer = null;
		}, 3400);
	}

	function applyConfigPayload(
		raw: RuntimeConfig & {
			secrets?: SecretStatusItem[];
			sandbox_workspace?: string;
			features?: RuntimeConfig['features'] & {
				voice_enabled?: boolean;
				bash_tools_enabled?: boolean;
			};
		}
	): void {
		providers = (raw.providers ?? []).map((p) => {
			const ext = p as ProviderConfig & { secret_key?: string };
			return {
				...p,
				secret_key: ext.secret_key || `${p.kind.toUpperCase()}_API_KEY`
			};
		});

		const baseSlots = createDefaultSlots();
		const incomingSlots = raw.model_slots ?? [];
		slots = baseSlots.map((def) => {
			const found = incomingSlots.find((s) => s.slot === def.wireSlot);
			if (!found) return def;
			const caps = (found.capabilities ?? {}) as Partial<Record<CapabilityKey, boolean>> & {
				tool_calls?: boolean;
			};
			return {
				...def,
				provider_id: found.provider_id ?? '',
				model_id: found.model_id ?? '',
				capabilities: {
					vision: Boolean(caps.vision ?? def.capabilities.vision),
					audio_in: Boolean(caps.audio_in ?? def.capabilities.audio_in),
					video_in: Boolean(caps.video_in ?? def.capabilities.video_in),
					tool_use: Boolean(caps.tool_use ?? caps.tool_calls ?? def.capabilities.tool_use),
					thinking: Boolean(caps.thinking ?? def.capabilities.thinking)
				}
			};
		});

		mcpServers = (raw.mcp_servers ?? []).map((srv) => {
			const ext = srv as MCPServerConfig & { url?: string };
			return {
				...srv,
				endpoint: srv.endpoint ?? ext.url ?? '',
				argsText: (srv.args ?? []).join(' ')
			};
		});

		if (raw.features) {
			features = {
				memory_enabled: Boolean(raw.features.memory_enabled),
				emotion_enabled: Boolean(raw.features.emotion_enabled),
				meme_collect_enabled: Boolean(raw.features.meme_collect_enabled),
				voice_enabled: Boolean(raw.features.voice_enabled ?? false),
				bash_enabled: Boolean(raw.features.bash_enabled ?? raw.features.bash_tools_enabled ?? false)
			};
		}

		if (raw.behavior) {
			behavior = {
				bot_name: raw.behavior.bot_name || DEFAULT_BEHAVIOR.bot_name,
				bot_aliases: raw.behavior.bot_aliases ?? DEFAULT_BEHAVIOR.bot_aliases,
				gate_threshold: Number(raw.behavior.gate_threshold ?? 0.7).toFixed(2),
				gate_at_threshold: Number(raw.behavior.gate_at_threshold ?? 0.3).toFixed(2),
				debounce_ms: String(Math.round(Number(raw.behavior.debounce_ms ?? 1400)))
			};
		}

		if (raw.sandbox_workspace) {
			sandboxWorkspace = raw.sandbox_workspace;
		}
		if (Array.isArray(raw.secrets)) {
			secrets = raw.secrets;
		}

		savedSnapshot = JSON.stringify({ providers, slots, mcpServers, features, behavior });
	}

	async function loadSecrets(signal?: AbortSignal): Promise<void> {
		try {
			const res = await fetch('/api/secrets', { signal });
			if (!res.ok) return;
			const data = (await res.json()) as SecretStatusItem[] | { items?: SecretStatusItem[] };
			if (Array.isArray(data)) {
				secrets = data;
			} else if (Array.isArray(data?.items)) {
				secrets = data.items;
			}
		} catch {
			// Optional endpoint when kernel is offline.
		}
	}

	async function loadConfig(signal?: AbortSignal): Promise<void> {
		loading = true;
		try {
			const [cfg] = await Promise.all([api.getConfig({ signal }), loadSecrets(signal)]);
			applyConfigPayload(cfg);
		} catch {
			// Standalone preview or offline kernel: preserve editable default structure.
			if (!savedSnapshot) {
				savedSnapshot = JSON.stringify({ providers, slots, mcpServers, features, behavior });
			}
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		const ctrl = new AbortController();
		void loadConfig(ctrl.signal);
		return () => {
			ctrl.abort();
			if (noticeTimer !== null) clearTimeout(noticeTimer);
		};
	});

	$effect(() => {
		const version = realtime.configVersion;
		if (version > 0 && !isDirty) {
			void loadConfig();
		}
	});

	function addProviderPreset(preset?: {
		name: string;
		kind: string;
		base_url: string;
		secret_key: string;
	}): void {
		const id = `prov_${Date.now().toString(36)}`;
		providers = [
			...providers,
			{
				id,
				name: preset?.name ?? '新供应商',
				kind: preset?.kind ?? 'gemini',
				base_url: preset?.base_url ?? 'https://generativelanguage.googleapis.com',
				secret_key: preset?.secret_key ?? 'GEMINI_API_KEY',
				has_api_key: false,
				api_key_hint: '',
				enabled: true
			}
		];
	}

	function removeProvider(id: string): void {
		providers = providers.filter((p) => p.id !== id);
	}

	async function writeSecret(key: string, rawValue: string): Promise<void> {
		const trimmedKey = key.trim();
		const trimmedVal = rawValue.trim();
		if (!trimmedKey || !trimmedVal) return;

		savingSecretKey = trimmedKey;
		try {
			const res = await fetch(`/api/secrets/${encodeURIComponent(trimmedKey)}`, {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ value: trimmedVal })
			});
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			const hint = trimmedVal.length > 8 ? `***${trimmedVal.slice(-4)}` : '***';
			// Immediately scrub raw secret from component state after write-only submission.
			secretInputs = { ...secretInputs, [trimmedKey]: '' };
			if (newSecretKey.trim() === trimmedKey) {
				newSecretKey = '';
				newSecretValue = '';
			}
			const existing = secrets.some((s) => s.key === trimmedKey);
			secrets = existing
				? secrets.map((s) =>
						s.key === trimmedKey ? { ...s, configured: true, masked_hint: hint } : s
					)
				: [...secrets, { key: trimmedKey, configured: true, masked_hint: hint }];
			providers = providers.map((p) =>
				p.secret_key === trimmedKey ? { ...p, has_api_key: true, api_key_hint: hint } : p
			);
			flash('mint', `密钥 ${trimmedKey} 已安全写入`);
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '写入密钥失败');
		} finally {
			savingSecretKey = null;
		}
	}

	function requestDeleteSecret(key: string): void {
		deleteSecretConfirm = key;
		deleteSecretDialogOpen = true;
	}

	async function confirmDeleteSecret(): Promise<void> {
		const key = deleteSecretConfirm;
		if (!key) return;
		deletingSecretKey = key;
		try {
			const res = await fetch(`/api/secrets/${encodeURIComponent(key)}`, {
				method: 'DELETE'
			});
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			secrets = secrets.filter((s) => s.key !== key);
			providers = providers.map((p) =>
				p.secret_key === key ? { ...p, has_api_key: false, api_key_hint: '' } : p
			);
			deleteSecretDialogOpen = false;
			deleteSecretConfirm = null;
			flash('amber', `密钥 ${key} 已清除`);
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '删除密钥失败');
		} finally {
			deletingSecretKey = null;
		}
	}

	function toggleCapability(slotKey: SlotKey, cap: CapabilityKey): void {
		slots = slots.map((s) =>
			s.key === slotKey
				? { ...s, capabilities: { ...s.capabilities, [cap]: !s.capabilities[cap] } }
				: s
		);
	}

	function addMcpServer(): void {
		const id = `mcp_${Date.now().toString(36)}`;
		mcpServers = [
			...mcpServers,
			{
				id,
				name: '新 MCP 服务',
				transport: 'streamable_http',
				endpoint: '',
				command: '',
				args: [],
				argsText: '',
				enabled: true,
				status: 'disconnected',
				tools_count: 0
			}
		];
	}

	function removeMcpServer(id: string): void {
		mcpServers = mcpServers.filter((s) => s.id !== id);
	}

	async function reloadMcpServer(id: string): Promise<void> {
		reloadingMcpId = id;
		try {
			const res = await fetch(`/api/mcp/${encodeURIComponent(id)}/reload`, {
				method: 'POST'
			});
			if (!res.ok) {
				const text = await res.text().catch(() => '');
				throw new Error(text.trim() || `HTTP ${res.status}`);
			}
			flash('sky', 'MCP 连接池已热重载');
			await loadConfig();
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '重载 MCP 失败');
		} finally {
			reloadingMcpId = null;
		}
	}

	async function saveAllConfig(): Promise<void> {
		saving = true;
		try {
			const patch: RuntimeConfigPatch = {
				providers: providers.map((p) => ({
					id: p.id,
					name: p.name.trim() || p.id,
					kind: p.kind,
					base_url: p.base_url.trim(),
					enabled: p.enabled
				})),
				model_slots: slots.map((s) => ({
					slot: s.wireSlot,
					provider_id: s.provider_id,
					model_id: s.model_id.trim(),
					capabilities: {
						vision: s.capabilities.vision,
						audio_in: s.capabilities.audio_in,
						video_in: s.capabilities.video_in,
						...(s.capabilities.tool_use ? { tool_use: true } : {}),
						...(s.capabilities.thinking ? { thinking: true } : {})
					} as ModelSlotConfig['capabilities']
				})),
				mcp_servers: mcpServers.map((m) => ({
					id: m.id,
					name: m.name.trim() || m.id,
					transport: m.transport,
					endpoint: m.transport === 'streamable_http' ? m.endpoint?.trim() : undefined,
					command: m.transport === 'stdio' ? m.command?.trim() : undefined,
					args:
						m.transport === 'stdio'
							? m.argsText
									.split(/\s+/)
									.map((x) => x.trim())
									.filter(Boolean)
							: undefined,
					enabled: m.enabled
				})),
				features: {
					memory_enabled: features.memory_enabled,
					emotion_enabled: features.emotion_enabled,
					meme_collect_enabled: features.meme_collect_enabled,
					bash_enabled: features.bash_enabled,
					voice_enabled: features.voice_enabled
				},
				behavior: {
					bot_name: behavior.bot_name.trim() || DEFAULT_BEHAVIOR.bot_name,
					bot_aliases: behavior.bot_aliases.trim(),
					gate_threshold: Math.min(1, Math.max(0.05, Number.parseFloat(behavior.gate_threshold) || 0.7)),
					gate_at_threshold: Math.min(1, Math.max(0.05, Number.parseFloat(behavior.gate_at_threshold) || 0.3)),
					debounce_ms: Math.min(10000, Math.max(0, Math.round(Number.parseFloat(behavior.debounce_ms) || 0)))
				}
			};

			const updated = await api.updateConfig(patch);
			applyConfigPayload(updated);
			flash('mint', '配置已热生效');
		} catch (err) {
			flash('rose', err instanceof Error ? err.message : '热重载保存失败');
		} finally {
			saving = false;
		}
	}

	function mcpStatusTone(status?: MCPServerConfig['status']): BadgeTone {
		if (status === 'connected') return 'mint';
		if (status === 'error') return 'rose';
		return 'neutral';
	}

	function mcpStatusLabel(status?: MCPServerConfig['status']): string {
		if (status === 'connected') return '已连接';
		if (status === 'error') return '异常';
		return '未连接';
	}
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="系统配置">
		{#snippet badge()}
			<div class="flex flex-wrap items-center gap-2">
				<Badge tone="mint" size="sm">热生效</Badge>
				{#if isDirty}
					<Badge tone="amber" size="sm" dot>未保存</Badge>
				{/if}
				{#if notice}
					<Badge tone={notice.tone} size="sm">{notice.text}</Badge>
				{/if}
			</div>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm" {loading} onclick={() => void loadConfig()}>
				<RefreshCw class="size-3.5" />
				<span>刷新</span>
			</Button>
			<Button variant="primary" size="sm" loading={saving} onclick={() => void saveAllConfig()}>
				<Save class="size-3.5" />
				<span>保存热生效</span>
			</Button>
		{/snippet}
	</PageHeader>

	<Tabs bind:value={tab} items={tabs}>
		{#snippet children(activeTab)}
			{#if activeTab === 'providers'}
				<div class="grid gap-4 lg:grid-cols-12">
					<!-- Providers Column -->
					<SurfaceCard variant="raised" padding="lg" class="space-y-4 lg:col-span-7">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex items-center gap-2.5">
								<span class="flex size-8 items-center justify-center rounded-xl bg-tint-sky text-sky-600">
									<Server class="size-4" />
								</span>
								<h2 class="text-sm font-semibold text-ink-primary">AI 供应商</h2>
							</div>
							<Button variant="tonal" size="sm" onclick={() => addProviderPreset()}>
								<Plus class="size-3.5" />
								<span>新增供应商</span>
							</Button>
						</div>

						{#if providers.length === 0}
							<div class="space-y-3 rounded-[var(--radius-panel)] bg-surface-subtle p-5 text-center">
								<p class="text-xs font-medium text-ink-secondary">快速添加常用供应商</p>
								<div class="flex flex-wrap justify-center gap-2">
									<Button
										variant="tonal"
										size="sm"
										onclick={() =>
											addProviderPreset({
												name: 'Google Gemini',
												kind: 'gemini',
												base_url: 'https://generativelanguage.googleapis.com',
												secret_key: 'GEMINI_API_KEY'
											})}
									>
										+ Gemini
									</Button>
									<Button
										variant="tonal"
										size="sm"
										onclick={() =>
											addProviderPreset({
												name: 'OpenAI Embedding',
												kind: 'openai',
												base_url: 'https://api.openai.com/v1',
												secret_key: 'OPENAI_API_KEY'
											})}
									>
										+ OpenAI
									</Button>
									<Button
										variant="tonal"
										size="sm"
										onclick={() =>
											addProviderPreset({
												name: 'Fish Audio',
												kind: 'fishaudio',
												base_url: 'https://api.fish.audio',
												secret_key: 'FISH_AUDIO_API_KEY'
											})}
									>
										+ Fish Audio
									</Button>
									<Button
										variant="tonal"
										size="sm"
										onclick={() =>
											addProviderPreset({
												name: '本地 Whisper',
												kind: 'whisper',
												base_url: 'http://127.0.0.1:9000',
												secret_key: ''
											})}
									>
										+ Whisper
									</Button>
								</div>
							</div>
						{:else}
							<div class="space-y-3">
								{#each providers as prov (prov.id)}
									<div class="space-y-3 rounded-[var(--radius-panel)] bg-surface-subtle p-4">
										<div class="flex flex-wrap items-center justify-between gap-2">
											<div class="min-w-0 flex-1">
												<Input bind:value={prov.name} placeholder="供应商名称" class="bg-surface-raised" />
											</div>
											<div class="flex shrink-0 items-center gap-2">
												<Switch
													bind:checked={prov.enabled}
													tone="mint"
													label={`启用 ${prov.name}`}
												/>
												<Button
													variant="ghost"
													size="icon"
													aria-label="移除供应商"
													onclick={() => removeProvider(prov.id)}
												>
													<Trash2 class="size-4 text-rose-500" />
												</Button>
											</div>
										</div>

										<div class="flex flex-wrap items-center gap-1.5">
											{#each PROVIDER_KINDS as kind (kind)}
												<button
													type="button"
													onclick={() => (prov.kind = kind)}
													class={cn(
														'cursor-pointer rounded-full px-2.5 py-0.5 font-mono text-[11px] font-medium transition-colors',
														prov.kind === kind
															? 'bg-sky-500 text-white'
															: 'bg-surface-raised text-ink-secondary hover:bg-tint-sky hover:text-sky-600'
													)}
												>
													{kind}
												</button>
											{/each}
										</div>

										<div class="grid gap-2.5 sm:grid-cols-2">
											<label class="space-y-1">
												<span class="text-[11px] font-medium text-ink-muted">BaseURL</span>
												<Input
													bind:value={prov.base_url}
													placeholder="https://..."
													class="bg-surface-raised"
												/>
											</label>
											<label class="space-y-1">
												<span class="flex items-center justify-between text-[11px] font-medium text-ink-muted">
													<span>密钥名 (Secret Key)</span>
													{#if prov.has_api_key}
														<span class="font-mono text-mint-700">{prov.api_key_hint || '***'}</span>
													{/if}
												</span>
												<Input
													bind:value={prov.secret_key}
													placeholder="GEMINI_API_KEY"
													class="bg-surface-raised font-mono"
												/>
											</label>
										</div>
									</div>
								{/each}
							</div>
						{/if}
					</SurfaceCard>

					<!-- Write-Only Secrets Column -->
					<SurfaceCard variant="raised" padding="lg" class="space-y-4 lg:col-span-5">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex items-center gap-2.5">
								<span class="flex size-8 items-center justify-center rounded-xl bg-tint-lilac text-lilac-600">
									<KeyRound class="size-4" />
								</span>
								<h2 class="text-sm font-semibold text-ink-primary">Write-Only 密钥库</h2>
							</div>
							<Badge tone="lilac" size="sm">只写脱敏</Badge>
						</div>

						<!-- Add new write-only secret -->
						<form
							class="space-y-2.5 rounded-[var(--radius-panel)] bg-tint-lilac/65 p-3.5"
							onsubmit={(e) => {
								e.preventDefault();
								void writeSecret(newSecretKey, newSecretValue);
							}}
						>
							<div class="grid gap-2 sm:grid-cols-2">
								<Input
									bind:value={newSecretKey}
									placeholder="KEY_NAME"
									class="bg-surface-raised font-mono text-xs"
								/>
								<Input
									type="password"
									autocomplete="new-password"
									bind:value={newSecretValue}
									placeholder="输入新密钥..."
									class="bg-surface-raised text-xs"
								/>
							</div>
							<div class="flex justify-end">
								<Button
									type="submit"
									variant="primary"
									size="sm"
									disabled={!newSecretKey.trim() || !newSecretValue.trim()}
									loading={savingSecretKey === newSecretKey.trim() && newSecretKey.trim() !== ''}
								>
									<KeyRound class="size-3.5" />
									<span>安全写入</span>
								</Button>
							</div>
						</form>

						<!-- Existing secrets list (redacted only) -->
						{#if mergedSecrets.length === 0}
							<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
								尚未录入密钥，写入后仅返回脱敏尾号
							</div>
						{:else}
							<div class="space-y-2.5">
								{#each mergedSecrets as sec (sec.key)}
									<div class="space-y-2.5 rounded-[var(--radius-panel)] bg-surface-subtle p-3.5">
										<div class="flex flex-wrap items-center justify-between gap-2">
											<span class="truncate font-mono text-xs font-semibold text-ink-primary">
												{sec.key}
											</span>
											<div class="flex items-center gap-1.5">
												<Badge tone={sec.configured ? 'mint' : 'neutral'} size="sm" dot={sec.configured}>
													{sec.configured ? '已配置' : '未配置'}
												</Badge>
												{#if sec.masked_hint}
													<span class="rounded-full bg-surface-raised px-2 py-0.5 font-mono text-[11px] text-ink-secondary">
														{sec.masked_hint}
													</span>
												{/if}
											</div>
										</div>

										<div class="flex items-center gap-2">
											<Input
												type="password"
												autocomplete="new-password"
												value={secretInputs[sec.key] ?? ''}
												oninput={(e) => {
													secretInputs = { ...secretInputs, [sec.key]: e.currentTarget.value };
												}}
												placeholder="覆盖写入新密钥..."
												class="h-9 flex-1 bg-surface-raised text-xs"
											/>
											<Button
												variant="tonal"
												size="sm"
												disabled={!(secretInputs[sec.key] ?? '').trim()}
												loading={savingSecretKey === sec.key}
												onclick={() => void writeSecret(sec.key, secretInputs[sec.key] ?? '')}
											>
												覆盖
											</Button>
											{#if sec.configured}
												<Button
													variant="ghost"
													size="icon"
													aria-label={`删除 ${sec.key}`}
													onclick={() => requestDeleteSecret(sec.key)}
												>
													<Trash2 class="size-3.5 text-rose-500" />
												</Button>
											{/if}
										</div>
									</div>
								{/each}
							</div>
						{/if}
					</SurfaceCard>
				</div>
			{:else if activeTab === 'slots'}
				<div class="grid gap-4 md:grid-cols-2">
					{#each slots as slot (slot.key)}
						{@const bound = Boolean(slot.provider_id && slot.model_id.trim())}
						<SurfaceCard variant="raised" padding="md" interactive class="space-y-3.5">
							<div class="flex flex-wrap items-center justify-between gap-2">
								<div class="flex items-center gap-2.5">
									<span class="flex size-8 items-center justify-center rounded-xl bg-tint-mint text-mint-600">
										<Cpu class="size-4" />
									</span>
									<div>
										<div class="text-sm font-semibold text-ink-primary">{slot.label}</div>
									</div>
								</div>
								<div class="flex items-center gap-1.5">
									<Badge tone={slot.tone} size="sm">{slot.key}</Badge>
									<Badge tone={bound ? 'mint' : 'neutral'} size="sm" dot={bound}>
										{bound ? '已绑定' : '未绑定'}
									</Badge>
								</div>
							</div>

							<div class="grid gap-2.5 sm:grid-cols-2">
								<label class="space-y-1">
									<span class="text-[11px] font-medium text-ink-muted">供应商</span>
									<select
										bind:value={slot.provider_id}
										class="h-10 w-full cursor-pointer rounded-[var(--radius-control)] bg-surface-subtle px-3 text-sm text-ink-primary transition-all hover:bg-surface-muted/70 focus:bg-surface-raised focus:ring-2 focus:ring-mint-500/35 focus:outline-none"
									>
										<option value="">选择供应商...</option>
										{#each providers as p (p.id)}
											<option value={p.id}>{p.name} ({p.kind})</option>
										{/each}
										{#if slot.provider_id && !providers.some((p) => p.id === slot.provider_id)}
											<option value={slot.provider_id}>{slot.provider_id}</option>
										{/if}
									</select>
								</label>

								<label class="space-y-1">
									<span class="text-[11px] font-medium text-ink-muted">Model ID</span>
									<Input bind:value={slot.model_id} placeholder="例如 gemini-2.5-flash" />
								</label>
							</div>

							<div class="flex flex-wrap items-center gap-1.5 pt-0.5">
								{#each CAPABILITY_ITEMS as cap (cap.key)}
									{@const on = slot.capabilities[cap.key]}
									<button
										type="button"
										onclick={() => toggleCapability(slot.key, cap.key)}
										class={cn(
											'cursor-pointer rounded-full px-2.5 py-1 font-mono text-[11px] font-medium transition-all duration-150',
											on
												? 'bg-tint-mint text-mint-700 shadow-[var(--shadow-xs)]'
												: 'bg-surface-subtle text-ink-muted hover:bg-surface-muted hover:text-ink-primary'
										)}
									>
										{cap.label}
									</button>
								{/each}
							</div>
						</SurfaceCard>
					{/each}
				</div>
			{:else if activeTab === 'mcp'}
				<div class="space-y-4">
					<!-- Group Chat Behavior, Gate Thresholds & Debounce (SQLite hot-reload) -->
					<SurfaceCard variant="raised" padding="lg" class="space-y-4">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex flex-wrap items-center gap-2.5">
								<span class="flex size-8 items-center justify-center rounded-xl bg-tint-mint text-mint-600">
									<SlidersHorizontal class="size-4" />
								</span>
								<h2 class="text-sm font-semibold text-ink-primary">群聊行为与门控</h2>
								<Badge tone="mint" size="sm">SQLite 热生效</Badge>
							</div>
							<Badge tone="neutral" size="sm">单槽最新聚合 · 无冷却计时器</Badge>
						</div>

						<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
							<label class="space-y-1">
								<span class="text-[11px] font-medium text-ink-muted">Bot 昵称</span>
								<Input bind:value={behavior.bot_name} placeholder="bk101" />
							</label>
							<label class="space-y-1">
								<span class="text-[11px] font-medium text-ink-muted">常用称呼（| 分隔）</span>
								<Input bind:value={behavior.bot_aliases} placeholder="bk101|101" />
							</label>
							<label class="space-y-1">
								<span class="text-[11px] font-medium text-ink-muted">自由接话阈值 (0~1)</span>
								<Input
									type="number"
									min="0.05"
									max="1"
									step="0.05"
									bind:value={behavior.gate_threshold}
								/>
							</label>
							<label class="space-y-1">
								<span class="text-[11px] font-medium text-ink-muted">@提及阈值 (0~1)</span>
								<Input
									type="number"
									min="0.05"
									max="1"
									step="0.05"
									bind:value={behavior.gate_at_threshold}
								/>
							</label>
							<label class="space-y-1">
								<span class="text-[11px] font-medium text-ink-muted">防抖聚合 (ms)</span>
								<Input
									type="number"
									min="0"
									max="10000"
									step="100"
									bind:value={behavior.debounce_ms}
								/>
							</label>
						</div>
					</SurfaceCard>

					<!-- Prominent Playful Sandboxed Bash & File Tools Toggle -->
					<SurfaceCard
						variant={features.bash_enabled ? 'tint-amber' : 'raised'}
						padding="lg"
						class="space-y-3.5"
					>
						<div class="flex flex-wrap items-center justify-between gap-4">
							<div class="flex min-w-0 items-center gap-3.5">
								<span
									class={cn(
										'flex size-11 shrink-0 items-center justify-center rounded-2xl transition-colors',
										features.bash_enabled
											? 'bg-amber-500 text-white shadow-[0_8px_18px_-6px_rgba(242,162,27,0.7)]'
											: 'bg-tint-amber text-amber-600'
									)}
								>
									<Terminal class="size-5" />
								</span>
								<div class="min-w-0">
									<div class="flex flex-wrap items-center gap-2">
										<h2 class="text-base font-bold tracking-tight text-ink-primary">
											沙箱 Bash 与文件工具集
										</h2>
										<Badge
											tone={features.bash_enabled ? 'amber' : 'neutral'}
											size="sm"
											dot
											pulse={features.bash_enabled}
										>
											{features.bash_enabled ? '已启用 · 受限沙箱' : '默认关闭'}
										</Badge>
									</div>
								</div>
							</div>

							<Switch
								bind:checked={features.bash_enabled}
								tone="amber"
								label="一键切换沙箱 Bash 与文件工具集"
							/>
						</div>

						<div
							class={cn(
								'flex flex-wrap items-center justify-between gap-2.5 rounded-[var(--radius-panel)] px-3.5 py-2.5',
								features.bash_enabled ? 'bg-surface-raised/80' : 'bg-surface-subtle'
							)}
						>
							<div class="flex min-w-0 items-center gap-2 font-mono text-xs text-ink-primary">
								<FolderLock class="size-4 shrink-0 text-amber-600" />
								<span class="truncate">{sandboxWorkspace}</span>
							</div>
							<div class="flex flex-wrap items-center gap-1.5">
								<Badge tone="mint" size="sm">
									<ShieldCheck class="size-3" />
									<span>路径隔离</span>
								</Badge>
								<Badge tone="sky" size="sm">环境变量脱敏</Badge>
								<Badge tone="lilac" size="sm">超时强杀</Badge>
							</div>
						</div>
					</SurfaceCard>

					<!-- Subsystem Feature Toggles -->
					<div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
						<SurfaceCard variant="raised" padding="md">
							<div class="flex items-center justify-between gap-3">
								<div class="flex items-center gap-2.5">
									<span class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-tint-lilac text-lilac-600">
										<Brain class="size-4" />
									</span>
									<span class="text-sm font-semibold text-ink-primary">联想记忆</span>
								</div>
								<Switch bind:checked={features.memory_enabled} tone="mint" label="切换联想记忆" />
							</div>
						</SurfaceCard>

						<SurfaceCard variant="raised" padding="md">
							<div class="flex items-center justify-between gap-3">
								<div class="flex items-center gap-2.5">
									<span class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-tint-peach text-coral-600">
										<HeartPulse class="size-4" />
									</span>
									<span class="text-sm font-semibold text-ink-primary">动态情绪</span>
								</div>
								<Switch bind:checked={features.emotion_enabled} tone="coral" label="切换动态情绪" />
							</div>
						</SurfaceCard>

						<SurfaceCard variant="raised" padding="md">
							<div class="flex items-center justify-between gap-3">
								<div class="flex items-center gap-2.5">
									<span class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-tint-amber text-amber-600">
										<SmilePlus class="size-4" />
									</span>
									<span class="text-sm font-semibold text-ink-primary">表情收录</span>
								</div>
								<Switch
									bind:checked={features.meme_collect_enabled}
									tone="amber"
									label="切换表情收录"
								/>
							</div>
						</SurfaceCard>

						<SurfaceCard variant="raised" padding="md">
							<div class="flex items-center justify-between gap-3">
								<div class="flex items-center gap-2.5">
									<span class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-tint-sky text-sky-600">
										<AudioLines class="size-4" />
									</span>
									<span class="text-sm font-semibold text-ink-primary">语音回复</span>
								</div>
								<Switch bind:checked={features.voice_enabled} tone="mint" label="切换语音回复" />
							</div>
						</SurfaceCard>
					</div>

					<!-- MCP Servers Panel -->
					<SurfaceCard variant="raised" padding="lg" class="space-y-4">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<div class="flex flex-wrap items-center gap-2.5">
								<span class="flex size-8 items-center justify-center rounded-xl bg-tint-sky text-sky-600">
									<Plug class="size-4" />
								</span>
								<h2 class="text-sm font-semibold text-ink-primary">MCP 外部扩展</h2>
								<Badge tone="neutral" size="sm">Sampling / Elicitation 已关闭</Badge>
							</div>
							<Button variant="tonal" size="sm" onclick={addMcpServer}>
								<Plus class="size-3.5" />
								<span>新增 MCP</span>
							</Button>
						</div>

						{#if mcpServers.length === 0}
							<div class="rounded-[var(--radius-panel)] bg-surface-subtle px-4 py-8 text-center text-xs text-ink-muted">
								暂无外部 MCP Server，支持 Streamable HTTP 与本机 stdio 接入
							</div>
						{:else}
							<div class="grid gap-3 lg:grid-cols-2">
								{#each mcpServers as srv (srv.id)}
									<div class="space-y-3 rounded-[var(--radius-panel)] bg-surface-subtle p-4">
										<div class="flex flex-wrap items-center justify-between gap-2">
											<div class="min-w-0 flex-1">
												<Input bind:value={srv.name} placeholder="服务名称" class="bg-surface-raised" />
											</div>
											<div class="flex shrink-0 items-center gap-1.5">
												<Switch
													bind:checked={srv.enabled}
													tone="mint"
													label={`启用 ${srv.name}`}
												/>
												<Button
													variant="ghost"
													size="icon"
													aria-label="热重载 MCP"
													loading={reloadingMcpId === srv.id}
													onclick={() => void reloadMcpServer(srv.id)}
												>
													<RotateCw class="size-4" />
												</Button>
												<Button
													variant="ghost"
													size="icon"
													aria-label="删除 MCP"
													onclick={() => removeMcpServer(srv.id)}
												>
													<Trash2 class="size-4 text-rose-500" />
												</Button>
											</div>
										</div>

										<div class="flex flex-wrap items-center justify-between gap-2">
											<div class="inline-flex items-center gap-1 rounded-[var(--radius-control)] bg-surface-raised p-1">
												<button
													type="button"
													onclick={() => (srv.transport = 'streamable_http')}
													class={cn(
														'cursor-pointer rounded-[calc(var(--radius-control)-4px)] px-2.5 py-1 text-xs font-medium transition-colors',
														srv.transport === 'streamable_http'
															? 'bg-tint-sky text-sky-600'
															: 'text-ink-muted hover:text-ink-primary'
													)}
												>
													Streamable HTTP
												</button>
												<button
													type="button"
													onclick={() => (srv.transport = 'stdio')}
													class={cn(
														'cursor-pointer rounded-[calc(var(--radius-control)-4px)] px-2.5 py-1 text-xs font-medium transition-colors',
														srv.transport === 'stdio'
															? 'bg-tint-lilac text-lilac-600'
															: 'text-ink-muted hover:text-ink-primary'
													)}
												>
													stdio
												</button>
											</div>

											<div class="flex items-center gap-1.5">
												<Badge tone={mcpStatusTone(srv.status)} size="sm" dot>
													{mcpStatusLabel(srv.status)}
												</Badge>
												{#if typeof srv.tools_count === 'number'}
													<Badge tone="neutral" size="sm">{srv.tools_count} 工具</Badge>
												{/if}
											</div>
										</div>

										{#if srv.transport === 'streamable_http'}
											<label class="block space-y-1">
												<span class="text-[11px] font-medium text-ink-muted">Endpoint URL</span>
												<Input
													bind:value={srv.endpoint}
													placeholder="https://example.com/mcp"
													class="bg-surface-raised font-mono text-xs"
												/>
											</label>
										{:else}
											<div class="grid gap-2.5 sm:grid-cols-2">
												<label class="block space-y-1">
													<span class="text-[11px] font-medium text-ink-muted">Command</span>
													<Input
														bind:value={srv.command}
														placeholder="npx"
														class="bg-surface-raised font-mono text-xs"
													/>
												</label>
												<label class="block space-y-1">
													<span class="text-[11px] font-medium text-ink-muted">Args（空格分隔）</span>
													<Input
														bind:value={srv.argsText}
														placeholder="-y @modelcontextprotocol/server"
														class="bg-surface-raised font-mono text-xs"
													/>
												</label>
											</div>
										{/if}
									</div>
								{/each}
							</div>
						{/if}
					</SurfaceCard>
				</div>
			{/if}
		{/snippet}
	</Tabs>

	<!-- Delete Secret Confirmation Dialog -->
	<Dialog bind:open={deleteSecretDialogOpen} title="确认删除密钥" size="sm">
		{#if deleteSecretConfirm}
			<div class="space-y-3">
				<div class="rounded-[var(--radius-panel)] bg-tint-rose p-3 font-mono text-xs font-semibold text-rose-600">
					{deleteSecretConfirm}
				</div>
				<p class="text-xs leading-relaxed text-ink-secondary">
					删除后绑定该密钥的供应商将无法鉴权，可随时重新写入。
				</p>
			</div>
		{/if}

		{#snippet footer()}
			<Button variant="ghost" size="sm" onclick={() => (deleteSecretDialogOpen = false)}>
				取消
			</Button>
			<Button
				variant="danger"
				size="sm"
				loading={deletingSecretKey !== null}
				onclick={() => void confirmDeleteSecret()}
			>
				<Trash2 class="size-3.5" />
				<span>确认删除</span>
			</Button>
		{/snippet}
	</Dialog>
</div>
