<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn } from './utils';

	interface Props {
		title: string;
		hint?: string;
		tone?: 'mint' | 'peach' | 'sky' | 'lilac' | 'amber';
		icon?: Snippet;
		action?: Snippet;
		class?: string;
	}

	let { title, hint, tone = 'mint', icon, action, class: className }: Props = $props();

	const bubbleClasses = {
		mint: 'bg-tint-mint text-mint-600',
		peach: 'bg-tint-peach text-coral-500',
		sky: 'bg-tint-sky text-sky-500',
		lilac: 'bg-tint-lilac text-lilac-500',
		amber: 'bg-tint-amber text-amber-500'
	};
</script>

<div
	class={cn(
		'flex flex-col items-center justify-center gap-3 rounded-[var(--radius-card)] bg-surface-raised px-6 py-12 text-center shadow-[var(--shadow-card)]',
		className
	)}
>
	{#if icon}
		<div class={cn('flex size-12 items-center justify-center rounded-2xl', bubbleClasses[tone])}>
			{@render icon()}
		</div>
	{/if}
	<div class="space-y-1">
		<p class="text-sm font-semibold text-ink-primary">{title}</p>
		{#if hint}
			<p class="text-xs text-ink-muted">{hint}</p>
		{/if}
	</div>
	{#if action}
		<div class="pt-1">
			{@render action()}
		</div>
	{/if}
</div>
