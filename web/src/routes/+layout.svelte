<script lang="ts">
	import type { Snippet } from 'svelte';
	import '../app.css';
	import { AppShell } from '$lib/components/shell';
	import { realtime } from '$lib/stores/realtime.svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => {
		realtime.connect();
		return () => {
			realtime.disconnect();
		};
	});
</script>

<AppShell>
	{@render children()}
</AppShell>
