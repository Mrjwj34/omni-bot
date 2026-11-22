<script lang="ts">
	import { Spring } from 'svelte/motion';
	import { realtime } from '$lib/stores/realtime.svelte';
	import { cn } from '$lib/components/ui';

	interface Props {
		compact?: boolean;
		class?: string;
	}

	let { compact = false, class: className }: Props = $props();

	const orbScale = Spring.of(
		() => 0.92 + Math.min(0.22, Math.abs(realtime.padState.value.a) * 0.25),
		{ stiffness: 0.16, damping: 0.45 }
	);

	const orbGradient = $derived.by(() => {
		switch (realtime.moodTone) {
			case 'coral':
				return 'from-coral-400 to-amber-400';
			case 'mint':
				return 'from-mint-400 to-sky-400';
			case 'sky':
				return 'from-sky-400 to-lilac-400';
			case 'lilac':
				return 'from-lilac-400 to-coral-400';
			case 'rose':
				return 'from-rose-400 to-coral-500';
			case 'amber':
				return 'from-amber-400 to-mint-400';
			default:
				return 'from-mint-400 to-mint-500';
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
		'group inline-flex items-center gap-2.5 rounded-[var(--radius-panel)] bg-surface-subtle/90 px-2.5 py-2 transition-colors hover:bg-surface-muted/80',
		compact && 'justify-center px-2',
		className
	)}
>
	<span
		class={cn(
			'relative flex size-6 shrink-0 items-center justify-center rounded-full bg-gradient-to-br shadow-xs',
			orbGradient
		)}
		style:transform={`scale(${orbScale.current})`}
	>
		<span class="size-2 rounded-full bg-white/80"></span>
	</span>

	{#if !compact}
		<div class="min-w-0 flex-1 leading-tight">
			<div class="truncate text-xs font-semibold text-ink-primary">{realtime.moodLabel}</div>
			<div class="mt-0.5 truncate font-mono text-[10px] text-ink-muted">
				P{fmt(realtime.padState.value.p)} A{fmt(realtime.padState.value.a)} D{fmt(realtime.padState.value.d)}
			</div>
		</div>
	{/if}
</a>
