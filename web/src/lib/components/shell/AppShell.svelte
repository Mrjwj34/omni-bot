<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { Menu, PanelLeftClose, PanelLeftOpen, Sparkles } from '@lucide/svelte';
	import { Badge, Button, Dialog, cn } from '$lib/components/ui';
	import { realtime } from '$lib/stores/realtime.svelte';
	import PADMiniPill from './PADMiniPill.svelte';
	import { isNavActive, NAV_ITEMS } from './nav';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();

	let collapsed = $state(false);
	let mobileMenuOpen = $state(false);

	let pathname = $derived(page.url.pathname);
	let mobilePrimaryItems = $derived(NAV_ITEMS.filter((item) => item.primaryMobile));

	let statusMeta = $derived.by(() => {
		switch (realtime.connectionStatus) {
			case 'connected':
				return { label: '实时在线', tone: 'mint' as const, pulse: true };
			case 'connecting':
			case 'reconnecting':
				return { label: '连接中', tone: 'amber' as const, pulse: true };
			default:
				return { label: '待机离线', tone: 'neutral' as const, pulse: false };
		}
	});
</script>

<div class="min-h-dvh w-full">
	<!-- Mobile top header -->
	<header class="sticky top-0 z-30 px-3 pt-3 lg:hidden">
		<div
			class="flex items-center justify-between gap-2 rounded-[var(--radius-card)] bg-surface-raised/90 px-3.5 py-2.5 shadow-[var(--shadow-card)] backdrop-blur-md"
		>
			<a href="/" class="flex min-w-0 items-center gap-2.5">
				<span
					class="flex size-8 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-mint-400 to-mint-600 text-white shadow-xs"
				>
					<Sparkles class="size-4" />
				</span>
				<span class="truncate text-sm font-bold tracking-tight text-ink-primary">omni-bot</span>
			</a>

			<div class="flex shrink-0 items-center gap-2">
				<Badge tone={statusMeta.tone} size="sm" dot pulse={statusMeta.pulse}>
					{statusMeta.label}
				</Badge>
				<Button
					variant="tonal"
					size="icon"
					aria-label="打开全部页面"
					onclick={() => (mobileMenuOpen = true)}
				>
					<Menu class="size-4" />
				</Button>
			</div>
		</div>
	</header>

	<div class="mx-auto flex w-full max-w-[1520px] gap-4 p-3 sm:p-4 lg:p-5">
		<!-- Desktop floating rail / sidebar -->
		<aside
			class={cn(
				'sticky top-5 hidden h-[calc(100dvh-2.5rem)] shrink-0 flex-col justify-between rounded-[var(--radius-card)] bg-surface-raised/90 p-3 shadow-[var(--shadow-card)] backdrop-blur-md transition-[width] duration-200 ease-out lg:flex',
				collapsed ? 'w-[78px]' : 'w-[236px]'
			)}
		>
			<div class="flex flex-col gap-5">
				<div class={cn('flex items-center', collapsed ? 'justify-center' : 'justify-between px-1.5')}>
					<a href="/" class="flex min-w-0 items-center gap-2.5">
						<span
							class="relative flex size-9 shrink-0 items-center justify-center rounded-2xl bg-gradient-to-br from-mint-400 to-mint-600 text-white shadow-[0_6px_16px_-6px_rgba(24,184,136,0.6)]"
						>
							<Sparkles class="size-4" />
							<span class="absolute -top-0.5 -right-0.5 size-2.5 rounded-full bg-coral-400"></span>
						</span>
						{#if !collapsed}
							<div class="min-w-0">
								<div class="truncate text-base font-bold tracking-tight text-ink-primary">omni-bot</div>
								<div class="truncate text-[11px] font-medium text-ink-muted">Kernel Studio</div>
							</div>
						{/if}
					</a>

					{#if !collapsed}
						<Button
							variant="ghost"
							size="icon"
							aria-label="收起侧栏"
							onclick={() => (collapsed = true)}
						>
							<PanelLeftClose class="size-4" />
						</Button>
					{/if}
				</div>

				{#if collapsed}
					<div class="flex justify-center">
						<Button
							variant="ghost"
							size="icon"
							aria-label="展开侧栏"
							onclick={() => (collapsed = false)}
						>
							<PanelLeftOpen class="size-4" />
						</Button>
					</div>
				{/if}

				<nav aria-label="主导航" class="flex flex-col gap-1">
					{#each NAV_ITEMS as item (item.href)}
						{@const active = isNavActive(pathname, item.href)}
						{@const Icon = item.icon}
						<a
							href={item.href}
							title={collapsed ? item.label : undefined}
							aria-current={active ? 'page' : undefined}
							class={cn(
								'group flex items-center gap-3 rounded-[var(--radius-panel)] px-3 py-2.5 text-sm font-medium transition-all duration-150',
								collapsed && 'justify-center px-0',
								active
									? 'bg-tint-mint text-mint-700 shadow-[var(--shadow-xs)]'
									: 'text-ink-secondary hover:bg-surface-subtle hover:text-ink-primary'
							)}
						>
							<Icon
								class={cn(
									'size-[18px] shrink-0 transition-transform duration-150 group-hover:scale-105',
									active ? 'text-mint-600' : 'text-ink-muted group-hover:text-ink-primary'
								)}
							/>
							{#if !collapsed}
								<span class="truncate">{item.label}</span>
							{/if}
						</a>
					{/each}
				</nav>
			</div>

			<div class="flex flex-col gap-2.5">
				<PADMiniPill compact={collapsed} />

				<div
					class={cn(
						'flex items-center rounded-[var(--radius-panel)] bg-surface-subtle/75 px-2.5 py-2',
						collapsed ? 'justify-center' : 'justify-between'
					)}
				>
					{#if collapsed}
						<span
							title={statusMeta.label}
							class={cn(
								'size-2.5 rounded-full',
								statusMeta.tone === 'mint' && 'bg-mint-500',
								statusMeta.tone === 'amber' && 'animate-pulse bg-amber-500',
								statusMeta.tone === 'neutral' && 'bg-ink-muted'
							)}
						></span>
					{:else}
						<Badge tone={statusMeta.tone} size="sm" dot pulse={statusMeta.pulse}>
							{statusMeta.label}
						</Badge>
						<span class="font-mono text-[11px] text-ink-muted">
							{realtime.adapters.length} 通道
						</span>
					{/if}
				</div>
			</div>
		</aside>

		<!-- Main content stage -->
		<main class="min-w-0 flex-1 pb-24 lg:pb-6">
			{@render children()}
		</main>
	</div>

	<!-- Mobile bottom floating dock -->
	<nav
		aria-label="底部快捷导航"
		class="fixed right-3 bottom-3 left-3 z-30 lg:hidden"
	>
		<div
			class="mx-auto grid max-w-md grid-cols-5 items-center gap-1 rounded-[var(--radius-card)] bg-surface-raised/95 p-1.5 shadow-[var(--shadow-float)] backdrop-blur-md"
		>
			{#each mobilePrimaryItems as item (item.href)}
				{@const active = isNavActive(pathname, item.href)}
				{@const Icon = item.icon}
				<a
					href={item.href}
					aria-current={active ? 'page' : undefined}
					class={cn(
						'flex flex-col items-center justify-center gap-0.5 rounded-[var(--radius-panel)] py-1.5 text-[11px] font-medium transition-colors',
						active ? 'bg-tint-mint text-mint-700' : 'text-ink-muted hover:text-ink-primary'
					)}
				>
					<Icon class="size-4" />
					<span class="whitespace-nowrap">{item.shortLabel}</span>
				</a>
			{/each}
		</div>
	</nav>

	<!-- Mobile drawer modal for all routes & live PAD status -->
	<Dialog bind:open={mobileMenuOpen} title="导航与状态" size="sm">
		<div class="flex flex-col gap-3">
			<PADMiniPill />
			<div class="grid grid-cols-2 gap-2">
				{#each NAV_ITEMS as item (item.href)}
					{@const active = isNavActive(pathname, item.href)}
					{@const Icon = item.icon}
					<a
						href={item.href}
						onclick={() => (mobileMenuOpen = false)}
						class={cn(
							'flex items-center gap-2.5 rounded-[var(--radius-panel)] px-3 py-2.5 text-sm font-medium transition-colors',
							active
								? 'bg-tint-mint text-mint-700'
								: 'bg-surface-subtle text-ink-secondary hover:bg-surface-muted'
						)}
					>
						<Icon class="size-4 shrink-0" />
						<span class="truncate">{item.label}</span>
					</a>
				{/each}
			</div>
		</div>
	</Dialog>
</div>
