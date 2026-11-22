<script lang="ts">
	import { HeartHandshake } from '@lucide/svelte';
	import { Badge, EmptyState, PageHeader, Tabs, type TabItem } from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	let tab = $state('layers');

	const tabs: TabItem[] = [
		{ value: 'layers', label: '三层人格' },
		{ value: 'pad', label: '气质 PAD' },
		{ value: 'snapshots', label: '快照 Diff' }
	];
</script>

<div class="mx-auto max-w-6xl space-y-5">
	<PageHeader title="人格情绪">
		{#snippet badge()}
			<Badge tone={realtime.moodTone} size="sm" dot>{realtime.moodLabel}</Badge>
		{/snippet}
	</PageHeader>

	<Tabs bind:value={tab} items={tabs}>
		{#snippet children()}
			<EmptyState title="人格与情绪配置就绪" hint="支持冻结信念、慢变性格条目与 PAD 动力参数调节" tone="peach">
				{#snippet icon()}
					<HeartHandshake class="size-5" />
				{/snippet}
			</EmptyState>
		{/snippet}
	</Tabs>
</div>
