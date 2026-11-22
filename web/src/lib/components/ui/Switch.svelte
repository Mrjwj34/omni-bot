<script lang="ts">
	import { Switch as BitsSwitch } from 'bits-ui';
	import { cn } from './utils';

	interface Props {
		checked?: boolean;
		disabled?: boolean;
		name?: string;
		id?: string;
		tone?: 'mint' | 'coral' | 'amber';
		label?: string;
		onCheckedChange?: (checked: boolean) => void;
		class?: string;
	}

	let {
		checked = $bindable(false),
		disabled = false,
		name,
		id,
		tone = 'mint',
		label,
		onCheckedChange,
		class: className
	}: Props = $props();

	const activeTrackClasses = {
		mint: 'data-[state=checked]:bg-mint-500',
		coral: 'data-[state=checked]:bg-coral-500',
		amber: 'data-[state=checked]:bg-amber-500'
	};
</script>

<BitsSwitch.Root
	bind:checked
	{disabled}
	{name}
	{id}
	aria-label={label}
	{onCheckedChange}
	class={cn(
		'peer inline-flex h-7 w-12 shrink-0 cursor-pointer items-center rounded-full bg-surface-muted p-1 transition-colors duration-200',
		'focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-mint-500/30',
		'disabled:cursor-not-allowed disabled:opacity-50',
		activeTrackClasses[tone],
		className
	)}
>
	<BitsSwitch.Thumb
		class={cn(
			'pointer-events-none block size-5 rounded-full bg-surface-raised shadow-[var(--shadow-xs)]',
			'transition-transform duration-200 ease-[cubic-bezier(0.34,1.56,0.64,1)]',
			'data-[state=checked]:translate-x-5 data-[state=unchecked]:translate-x-0'
		)}
	/>
</BitsSwitch.Root>
