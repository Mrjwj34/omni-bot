<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { cn } from './utils';

	interface Props extends Omit<HTMLInputAttributes, 'value' | 'class'> {
		value?: string;
		leading?: Snippet;
		trailing?: Snippet;
		class?: string;
		inputClass?: string;
	}

	let {
		value = $bindable(''),
		leading,
		trailing,
		class: className,
		inputClass,
		...rest
	}: Props = $props();
</script>

<div
	class={cn(
		'flex h-10 w-full items-center gap-2 rounded-[var(--radius-control)] bg-surface-subtle px-3.5 text-sm text-ink-primary',
		'transition-all duration-150 hover:bg-surface-muted/70',
		'focus-within:bg-surface-raised focus-within:shadow-[var(--shadow-xs)] focus-within:ring-2 focus-within:ring-mint-500/35',
		className
	)}
>
	{#if leading}
		<span class="flex shrink-0 items-center text-ink-muted">
			{@render leading()}
		</span>
	{/if}
	<input
		bind:value
		class={cn(
			'min-w-0 flex-1 bg-transparent text-sm text-ink-primary placeholder:text-ink-muted focus:outline-none disabled:cursor-not-allowed disabled:opacity-50',
			inputClass
		)}
		{...rest}
	/>
	{#if trailing}
		<span class="flex shrink-0 items-center text-ink-muted">
			{@render trailing()}
		</span>
	{/if}
</div>
