<script lang="ts">
	import { Activity } from '@lucide/svelte';
	import { Badge, EmptyState, PageHeader, Tabs, type TabItem } from '$lib/components/ui';

	let filter = $state('all');

	const tabs: TabItem[] = [
		{ value: 'all', label: '全部' },
		{ value: 'completed', label: '已回复' },
		{ value: 'gated_out', label: '门控静音' },
		{ value: 'error', label: '异常' }
	];
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="Run 轨迹">
		{#snippet badge()}
			<Badge tone="sky" size="sm">实时追踪</Badge>
		{/snippet}
	</PageHeader>

	<Tabs bind:value={filter} items={tabs}>
		{#snippet children()}
			<EmptyState title="暂无 Run 轨迹记录" hint="触发群聊回复或对话调试后将实时呈现" tone="sky">
				{#snippet icon()}
					<Activity class="size-5" />
				{/snippet}
			</EmptyState>
		{/snippet}
	</Tabs>
</div>
