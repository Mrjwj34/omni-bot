<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Dialog as BitsDialog } from 'bits-ui';
	import { X } from '@lucide/svelte';
	import { cn } from './utils';

	interface Props {
		open?: boolean;
		title: string;
		size?: 'sm' | 'md' | 'lg';
		onOpenChange?: (open: boolean) => void;
		trigger?: Snippet;
		footer?: Snippet;
		children: Snippet;
		class?: string;
	}

	let {
		open = $bindable(false),
		title,
		size = 'md',
		onOpenChange,
		trigger,
		footer,
		children,
		class: className
	}: Props = $props();

	const sizeClasses = {
		sm: 'max-w-sm',
		md: 'max-w-lg',
		lg: 'max-w-2xl'
	};
</script>

<BitsDialog.Root bind:open {onOpenChange}>
	{#if trigger}
		<BitsDialog.Trigger>
			{#snippet child({ props })}
				<span {...props} class="inline-flex">
					{@render trigger()}
				</span>
			{/snippet}
		</BitsDialog.Trigger>
	{/if}

	<BitsDialog.Portal>
		<BitsDialog.Overlay
			class="animate-fade-in fixed inset-0 z-50 bg-ink-primary/20 backdrop-blur-md"
		/>
		<BitsDialog.Content
			class={cn(
				'animate-pop-in fixed top-1/2 left-1/2 z-50 w-[calc(100%-2rem)] -translate-x-1/2 -translate-y-1/2',
				'rounded-[var(--radius-card)] bg-surface-pop p-6 shadow-[var(--shadow-pop)] focus:outline-none',
				sizeClasses[size],
				className
			)}
		>
			<div class="flex items-center justify-between gap-3 pb-4">
				<BitsDialog.Title class="text-lg font-semibold tracking-tight text-ink-primary">
					{title}
				</BitsDialog.Title>
				<BitsDialog.Close
					aria-label="关闭"
					class="inline-flex size-8 cursor-pointer items-center justify-center rounded-full bg-surface-subtle text-ink-secondary transition-colors hover:bg-surface-muted hover:text-ink-primary"
				>
					<X class="size-4" />
				</BitsDialog.Close>
			</div>

			<div class="text-sm text-ink-secondary">
				{@render children()}
			</div>

			{#if footer}
				<div class="mt-6 flex items-center justify-end gap-2.5">
					{@render footer()}
				</div>
			{/if}
		</BitsDialog.Content>
	</BitsDialog.Portal>
</BitsDialog.Root>
