<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAttributes } from 'svelte/elements';
	import { cn } from './utils';

	export type SurfaceVariant =
		| 'raised'
		| 'subtle'
		| 'tint-mint'
		| 'tint-peach'
		| 'tint-sky'
		| 'tint-lilac'
		| 'tint-amber';

	export type SurfacePadding = 'none' | 'sm' | 'md' | 'lg';

	interface Props extends HTMLAttributes<HTMLDivElement> {
		variant?: SurfaceVariant;
		padding?: SurfacePadding;
		interactive?: boolean;
		children: Snippet;
	}

	let {
		variant = 'raised',
		padding = 'md',
		interactive = false,
		class: className,
		children,
		...rest
	}: Props = $props();

	const variantClasses: Record<SurfaceVariant, string> = {
		raised: 'bg-surface-raised shadow-[var(--shadow-card)]',
		subtle: 'bg-surface-subtle',
		'tint-mint': 'bg-tint-mint text-ink-primary',
		'tint-peach': 'bg-tint-peach text-ink-primary',
		'tint-sky': 'bg-tint-sky text-ink-primary',
		'tint-lilac': 'bg-tint-lilac text-ink-primary',
		'tint-amber': 'bg-tint-amber text-ink-primary'
	};

	const paddingClasses: Record<SurfacePadding, string> = {
		none: '',
		sm: 'p-3.5',
		md: 'p-5',
		lg: 'p-6 sm:p-7'
	};
</script>

<div
	class={cn(
		'rounded-[var(--radius-card)] transition-all duration-200',
		variantClasses[variant],
		paddingClasses[padding],
		interactive && 'hover:-translate-y-0.5 hover:shadow-[var(--shadow-float)]',
		className
	)}
	{...rest}
>
	{@render children()}
</div>
