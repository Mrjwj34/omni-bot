<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAttributes } from 'svelte/elements';
	import { cn } from './utils';

	export type BadgeTone = 'mint' | 'coral' | 'sky' | 'amber' | 'lilac' | 'rose' | 'neutral';

	interface Props extends HTMLAttributes<HTMLSpanElement> {
		tone?: BadgeTone;
		size?: 'sm' | 'md';
		dot?: boolean;
		pulse?: boolean;
		children: Snippet;
	}

	let {
		tone = 'neutral',
		size = 'md',
		dot = false,
		pulse = false,
		class: className,
		children,
		...rest
	}: Props = $props();

	const toneClasses: Record<BadgeTone, string> = {
		mint: 'bg-tint-mint text-mint-700',
		coral: 'bg-tint-peach text-coral-600',
		sky: 'bg-tint-sky text-sky-600',
		amber: 'bg-tint-amber text-amber-600',
		lilac: 'bg-tint-lilac text-lilac-600',
		rose: 'bg-tint-rose text-rose-600',
		neutral: 'bg-surface-subtle text-ink-secondary'
	};

	const dotClasses: Record<BadgeTone, string> = {
		mint: 'bg-mint-500',
		coral: 'bg-coral-500',
		sky: 'bg-sky-500',
		amber: 'bg-amber-500',
		lilac: 'bg-lilac-500',
		rose: 'bg-rose-500',
		neutral: 'bg-ink-muted'
	};

	const sizeClasses = {
		sm: 'px-2.5 py-0.5 text-xs gap-1.5',
		md: 'px-3 py-1 text-xs gap-1.5'
	};
</script>

<span
	class={cn(
		'inline-flex shrink-0 items-center rounded-[var(--radius-pill)] font-medium tracking-tight whitespace-nowrap',
		toneClasses[tone],
		sizeClasses[size],
		className
	)}
	{...rest}
>
	{#if dot}
		<span class="relative inline-flex size-2 shrink-0">
			{#if pulse}
				<span
					class={cn('absolute inline-flex size-full animate-ping rounded-full opacity-60', dotClasses[tone])}
				></span>
			{/if}
			<span class={cn('relative inline-flex size-2 rounded-full', dotClasses[tone])}></span>
		</span>
	{/if}
	{@render children()}
</span>
