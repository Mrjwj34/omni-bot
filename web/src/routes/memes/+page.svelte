<script lang="ts">
	import { Search, SmilePlus } from '@lucide/svelte';
	import { Badge, EmptyState, Input, PageHeader } from '$lib/components/ui';

	let query = $state('');
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="表情金库">
		{#snippet badge()}
			<Badge tone="amber" size="sm">pHash 去重</Badge>
		{/snippet}
	</PageHeader>

	<Input bind:value={query} placeholder="按情绪标签或视觉描述筛选..." class="sm:max-w-sm">
		{#snippet leading()}
			<Search class="size-4" />
		{/snippet}
	</Input>

	<EmptyState title="表情金库暂无条目" hint="群聊收录或迁移旧版资产后可在此筛选与管理" tone="amber">
		{#snippet icon()}
			<SmilePlus class="size-5" />
		{/snippet}
	</EmptyState>
</div>
