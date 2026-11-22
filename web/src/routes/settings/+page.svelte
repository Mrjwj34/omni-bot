<script lang="ts">
	import { Settings2, ShieldAlert } from '@lucide/svelte';
	import { Badge, EmptyState, PageHeader, SurfaceCard, Switch, Tabs, type TabItem } from '$lib/components/ui';

	let tab = $state('providers');
	let bashEnabled = $state(false);

	const tabs: TabItem[] = [
		{ value: 'providers', label: '供应商与槽位' },
		{ value: 'mcp', label: 'MCP 扩展' },
		{ value: 'sandbox', label: '安全开关' }
	];
</script>

<div class="mx-auto max-w-5xl space-y-5">
	<PageHeader title="系统配置">
		{#snippet badge()}
			<Badge tone="mint" size="sm">热生效</Badge>
		{/snippet}
	</PageHeader>

	<Tabs bind:value={tab} items={tabs}>
		{#snippet children(activeTab)}
			{#if activeTab === 'sandbox'}
				<SurfaceCard variant="raised" padding="md">
					<div class="flex items-center justify-between gap-4">
						<div class="flex items-center gap-3">
							<span class="flex size-10 shrink-0 items-center justify-center rounded-2xl bg-tint-amber text-amber-600">
								<ShieldAlert class="size-5" />
							</span>
							<div>
								<div class="text-sm font-semibold text-ink-primary">受限 Bash 与文件工具集</div>
								<div class="text-xs text-ink-muted">默认关闭，启用后仅限沙箱工作区</div>
							</div>
						</div>
						<Switch bind:checked={bashEnabled} tone="amber" label="切换 Bash 工具集" />
					</div>
				</SurfaceCard>
			{:else}
				<EmptyState title="动态配置面板待接入" hint="支持 Write-Only 密钥、模型槽位与 MCP 热重载" tone="sky">
					{#snippet icon()}
						<Settings2 class="size-5" />
					{/snippet}
				</EmptyState>
			{/if}
		{/snippet}
	</Tabs>
</div>
