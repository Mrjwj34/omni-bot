<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Tabs as BitsTabs } from 'bits-ui';
	import { cn } from './utils';

	export interface TabItem {
		value: string;
		label: string;
		badge?: string | number;
		disabled?: boolean;
	}

	interface Props {
		value?: string;
		items: TabItem[];
		onValueChange?: (value: string) => void;
		class?: string;
		listClass?: string;
		children?: Snippet<[string]>;
	}

	let {
		value = $bindable(''),
		items,
		onValueChange,
		class: className,
		listClass,
		children
	}: Props = $props();

	let resolvedValue = $derived(value || items[0]?.value || '');
</script>

<BitsTabs.Root
	value={resolvedValue}
	onValueChange={(next) => {
		value = next;
		onValueChange?.(next);
	}}
	class={cn('flex flex-col gap-4', className)}
>
	<BitsTabs.List
		class={cn(
			'inline-flex w-fit max-w-full items-center gap-1 overflow-x-auto rounded-[var(--radius-panel)] bg-surface-subtle p-1',
			listClass
		)}
	>
		{#each items as item (item.value)}
			<BitsTabs.Trigger
				value={item.value}
				disabled={item.disabled}
				class={cn(
					'inline-flex shrink-0 cursor-pointer items-center gap-1.5 rounded-[calc(var(--radius-panel)-4px)] px-3.5 py-1.5 text-sm font-medium whitespace-nowrap text-ink-secondary',
					'transition-all duration-150 hover:text-ink-primary',
					'data-[state=active]:bg-surface-raised data-[state=active]:text-ink-primary data-[state=active]:shadow-[var(--shadow-xs)]',
					'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-mint-500/30',
					'disabled:pointer-events-none disabled:opacity-40'
				)}
			>
				<span>{item.label}</span>
				{#if item.badge !== undefined}
					<span class="rounded-full bg-surface-subtle px-1.5 py-0.5 text-[11px] font-semibold text-ink-muted">
						{item.badge}
					</span>
				{/if}
			</BitsTabs.Trigger>
		{/each}
	</BitsTabs.List>

	{#if children}
		{#each items as item (item.value)}
			<BitsTabs.Content value={item.value} class="focus-visible:outline-none">
				{@render children(item.value)}
			</BitsTabs.Content>
		{/each}
	{/if}
</BitsTabs.Root>
