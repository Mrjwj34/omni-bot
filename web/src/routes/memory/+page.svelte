<script lang="ts">
	import { Brain, Search, Sparkles } from '@lucide/svelte';
	import { Badge, Button, EmptyState, Input, PageHeader, Tabs, type TabItem } from '$lib/components/ui';

	let query = $state('');
	let tab = $state('recall');

	const tabs: TabItem[] = [
		{ value: 'recall', label: '联想检索' },
		{ value: 'timeline', label: '时序链' },
		{ value: 'dreaming', label: 'Dreaming' }
	];
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="记忆图谱">
		{#snippet badge()}
			<Badge tone="lilac" size="sm">FTS5 + 向量</Badge>
		{/snippet}
		{#snippet actions()}
			<Button variant="tonal" size="sm">
				<Sparkles class="size-3.5" />
				<span>触发巩固</span>
			</Button>
		{/snippet}
	</PageHeader>

	<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
		<Input bind:value={query} placeholder="搜索记忆线索或实体..." class="sm:max-w-sm">
			{#snippet leading()}
				<Search class="size-4" />
			{/snippet}
		</Input>
	</div>

	<Tabs bind:value={tab} items={tabs}>
		{#snippet children()}
			<EmptyState title="记忆库待接入" hint="支持双路召回、时序演化链与离线 Dreaming 面板" tone="lilac">
				{#snippet icon()}
					<Brain class="size-5" />
				{/snippet}
			</EmptyState>
		{/snippet}
	</Tabs>
</div>
