<script lang="ts">
	import { realtime } from '$lib/stores/realtime.svelte';
	import { cn } from '$lib/components/ui';

	interface Props {
		compact?: boolean;
		class?: string;
	}

	let { compact = false, class: className }: Props = $props();

	const dotColor = $derived.by(() => {
		switch (realtime.moodTone) {
			case 'coral':
				return 'bg-coral-500';
			case 'mint':
				return 'bg-mint-500';
			case 'sky':
				return 'bg-sky-500';
			case 'lilac':
				return 'bg-lilac-500';
			case 'rose':
				return 'bg-rose-500';
			case 'amber':
				return 'bg-amber-500';
			default:
				return 'bg-mint-500';
		}
	});

	function fmt(v: number): string {
		const sign = v > 0 ? '+' : '';
		return `${sign}${v.toFixed(2)}`;
	}
</script>

<a
	href="/persona"
	title={`P ${fmt(realtime.padState.value.p)} · A ${fmt(realtime.padState.value.a)} · D ${fmt(realtime.padState.value.d)}`}
	class={cn(
		'group flex items-center gap-2.5 rounded-[var(--radius-panel)] bg-surface-subtle/90 px-3 py-2 transition-colors hover:bg-surface-muted/80',
		compact && 'justify-center px-2',
		className
	)}
>
	{#if compact}
		<span class={cn('size-2.5 shrink-0 rounded-full', dotColor)}></span>
	{:else}
		<div class="min-w-0 flex-1 leading-tight">
			<div class="flex items-center gap-2">
				<span class={cn('size-2 shrink-0 rounded-full', dotColor)}></span>
				<span class="truncate text-xs font-semibold text-ink-primary">{realtime.moodLabel}</span>
			</div>
			<div class="mt-1 truncate pl-4 font-mono text-[10px] text-ink-muted">
				P{fmt(realtime.padState.value.p)} A{fmt(realtime.padState.value.a)} D{fmt(realtime.padState.value.d)}
			</div>
		</div>
	{/if}
</a>
