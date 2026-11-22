import {
	Activity,
	Brain,
	HeartHandshake,
	LayoutDashboard,
	MessageCircleHeart,
	Settings2,
	SmilePlus
} from '@lucide/svelte';
import type { Component } from 'svelte';

export interface NavItem {
	href: string;
	label: string;
	shortLabel: string;
	icon: Component<{ class?: string }>;
	accent: 'mint' | 'sky' | 'lilac' | 'coral' | 'amber';
	primaryMobile?: boolean;
}

export const NAV_ITEMS: NavItem[] = [
	{
		href: '/',
		label: '驾驶舱',
		shortLabel: '总览',
		icon: LayoutDashboard,
		accent: 'mint',
		primaryMobile: true
	},
	{
		href: '/traces',
		label: 'Run 轨迹',
		shortLabel: '轨迹',
		icon: Activity,
		accent: 'sky',
		primaryMobile: true
	},
	{
		href: '/memory',
		label: '记忆图谱',
		shortLabel: '记忆',
		icon: Brain,
		accent: 'lilac',
		primaryMobile: true
	},
	{
		href: '/persona',
		label: '人格情绪',
		shortLabel: '人格',
		icon: HeartHandshake,
		accent: 'coral',
		primaryMobile: false
	},
	{
		href: '/memes',
		label: '表情金库',
		shortLabel: '表情',
		icon: SmilePlus,
		accent: 'amber',
		primaryMobile: true
	},
	{
		href: '/chat',
		label: '对话调试',
		shortLabel: '调试',
		icon: MessageCircleHeart,
		accent: 'mint',
		primaryMobile: true
	},
	{
		href: '/settings',
		label: '系统配置',
		shortLabel: '配置',
		icon: Settings2,
		accent: 'sky',
		primaryMobile: false
	}
];

export function isNavActive(pathname: string, href: string): boolean {
	if (href === '/') return pathname === '/';
	return pathname === href || pathname.startsWith(`${href}/`);
}
