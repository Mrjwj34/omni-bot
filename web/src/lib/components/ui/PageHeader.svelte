<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn } from './utils';

	interface Props {
		title: string;
		badge?: Snippet;
		actions?: Snippet;
		class?: string;
	}

	let { title, badge, actions, class: className }: Props = $props();
</script>

<header class={cn('flex flex-wrap items-center justify-between gap-3 pb-5', className)}>
	<div class="flex min-w-0 items-center gap-2.5">
		<h1 class="truncate text-2xl font-bold tracking-tight text-ink-primary">{title}</h1>
		{#if badge}
			{@render badge()}
		{/if}
	</div>
	{#if actions}
		<div class="flex shrink-0 items-center gap-2">
			{@render actions()}
		</div>
	{/if}
</header>
