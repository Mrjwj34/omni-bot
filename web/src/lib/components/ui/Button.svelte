<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';
	import { cn } from './utils';

	export type ButtonVariant = 'primary' | 'tonal' | 'ghost' | 'danger';
	export type ButtonSize = 'sm' | 'md' | 'lg' | 'icon';

	type BaseProps = {
		variant?: ButtonVariant;
		size?: ButtonSize;
		loading?: boolean;
		class?: string;
		children: Snippet;
	};

	type ButtonAsButton = BaseProps &
		Omit<HTMLButtonAttributes, 'class'> & {
			href?: undefined;
		};

	type ButtonAsLink = BaseProps &
		Omit<HTMLAnchorAttributes, 'class'> & {
			href: string;
			disabled?: boolean;
		};

	type Props = ButtonAsButton | ButtonAsLink;

	let {
		variant = 'primary',
		size = 'md',
		loading = false,
		href,
		disabled = false,
		class: className,
		children,
		...rest
	}: Props = $props();

	const variantClasses: Record<ButtonVariant, string> = {
		primary:
			'bg-mint-500 text-white shadow-[0_6px_18px_-6px_rgba(24,184,136,0.55)] hover:bg-mint-600 focus-visible:ring-mint-500/35',
		tonal: 'bg-surface-subtle text-ink-primary hover:bg-surface-muted focus-visible:ring-mint-500/30',
		ghost: 'bg-transparent text-ink-secondary hover:bg-surface-subtle hover:text-ink-primary focus-visible:ring-mint-500/25',
		danger: 'bg-tint-rose text-rose-600 hover:bg-rose-500 hover:text-white focus-visible:ring-rose-500/30'
	};

	const sizeClasses: Record<ButtonSize, string> = {
		sm: 'h-8 px-3.5 text-xs gap-1.5 rounded-[var(--radius-pill)]',
		md: 'h-10 px-4 text-sm gap-2 rounded-[var(--radius-control)]',
		lg: 'h-11 px-5 text-sm gap-2 rounded-[var(--radius-panel)]',
		icon: 'size-9 rounded-[var(--radius-control)] p-0'
	};

	let computedClass = $derived(
		cn(
			'inline-flex shrink-0 cursor-pointer items-center justify-center font-medium whitespace-nowrap select-none',
			'transition-all duration-150 active:scale-[0.97]',
			'focus-visible:outline-none focus-visible:ring-3',
			'disabled:pointer-events-none disabled:opacity-50',
			variantClasses[variant],
			sizeClasses[size],
			className
		)
	);
</script>

{#if href}
	<a
		{href}
		aria-disabled={disabled || loading || undefined}
		class={computedClass}
		{...(rest as Omit<HTMLAnchorAttributes, 'class'>)}
	>
		{@render children()}
	</a>
{:else}
	<button
		type="button"
		disabled={disabled || loading}
		class={computedClass}
		{...(rest as Omit<HTMLButtonAttributes, 'class'>)}
	>
		{#if loading}
			<span
				class="size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
				aria-hidden="true"
			></span>
		{/if}
		{@render children()}
	</button>
{/if}
