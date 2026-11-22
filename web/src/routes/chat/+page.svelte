<script lang="ts">
	import { MessageCircleHeart, Send } from '@lucide/svelte';
	import { Badge, Button, EmptyState, Input, PageHeader, SurfaceCard } from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';

	let draft = $state('');
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-5">
	<PageHeader title="对话调试">
		{#snippet badge()}
			<Badge tone={realtime.connectionStatus === 'connected' ? 'mint' : 'neutral'} size="sm" dot>
				{realtime.connectionStatus === 'connected' ? 'WS 已连接' : 'WS 离线'}
			</Badge>
		{/snippet}
	</PageHeader>

	<EmptyState title="内建调试房间已就绪" hint="复用 Channel/Chat 协议直连 Kernel 对话测试" tone="mint">
		{#snippet icon()}
			<MessageCircleHeart class="size-5" />
		{/snippet}
	</EmptyState>

	<SurfaceCard variant="raised" padding="sm">
		<div class="flex items-center gap-2">
			<Input bind:value={draft} placeholder="输入消息发送至调试房间..." class="flex-1" />
			<Button variant="primary" disabled={!draft.trim()}>
				<Send class="size-4" />
				<span>发送</span>
			</Button>
		</div>
	</SurfaceCard>
</div>
