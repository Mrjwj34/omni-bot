<script lang="ts">
	import type { HTMLTextareaAttributes } from 'svelte/elements';
	import { cn } from './utils';

	interface Props extends Omit<HTMLTextareaAttributes, 'value' | 'class'> {
		value?: string;
		class?: string;
	}

	let { value = $bindable(''), rows = 3, class: className, ...rest }: Props = $props();
</script>

<textarea
	bind:value
	{rows}
	class={cn(
		'w-full resize-y rounded-[var(--radius-panel)] bg-surface-subtle px-3.5 py-2.5 text-sm text-ink-primary placeholder:text-ink-muted',
		'transition-all duration-150 hover:bg-surface-muted/70',
		'focus:bg-surface-raised focus:shadow-[var(--shadow-xs)] focus:outline-none focus:ring-2 focus:ring-mint-500/35',
		'disabled:cursor-not-allowed disabled:opacity-50',
		className
	)}
	{...rest}
></textarea>
