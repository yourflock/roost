<script lang="ts">
	import '../app.css';
	import { page } from '$app/stores';

	interface Props {
		data: {
			admin: { id: string; email: string; name: string; role: string } | null;
			sessionToken: string | null;
		};
		children?: import('svelte').Snippet;
	}

	let { data, children }: Props = $props();

	const isLoggedIn = $derived(!!data.admin);
	const currentPath = $derived($page.url.pathname);

	const navItems = [
		{ href: '/dashboard', label: 'Dashboard', icon: '📊' },
		{ href: '/channels', label: 'Channels', icon: '📺' },
		{ href: '/vod', label: 'VOD Catalog', icon: '🎬' },
		{ href: '/catchup', label: 'Catchup / DVR', icon: '⏺️' },
		{ href: '/subscribers', label: 'Subscribers', icon: '👥' },
		{ href: '/streams', label: 'Live Streams', icon: '🔴' },
		{ href: '/billing', label: 'Billing', icon: '💳' },
		{ href: '/epg', label: 'EPG Sources', icon: '📅' },
		{ href: '/sports', label: 'Sports', icon: '🏈' },
		{ href: '/system', label: 'System', icon: '🖥️' }
	];

	function isActive(href: string): boolean {
		if (href === '/dashboard') return currentPath === '/dashboard' || currentPath === '/';
		return currentPath.startsWith(href);
	}
</script>

{#if !isLoggedIn || currentPath === '/login'}
	<!-- Public layout: just render content -->
	{@render children?.()}
{:else}
	<!-- Admin layout with sidebar -->
	<div class="flex h-screen bg-slate-900 overflow-hidden">
		<!-- Sidebar -->
		<aside class="w-64 bg-slate-800 border-r border-slate-700 flex flex-col flex-shrink-0">
			<!-- Logo / Brand -->
			<div class="px-5 py-5 border-b border-slate-700">
				<div class="flex items-center gap-2.5">
					<div
						class="w-8 h-8 bg-roost-500 rounded-lg flex items-center justify-center text-white font-bold text-sm"
					>
						R
					</div>
					<div>
						<p class="font-semibold text-slate-100 text-sm">Roost Admin</p>
						<p class="text-xs text-slate-500">Management Console</p>
					</div>
				</div>
			</div>

			<!-- Navigation -->
			<nav class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
				{#each navItems as item}
					<a href={item.href} class="sidebar-link {isActive(item.href) ? 'active' : ''}">
						<span class="text-base">{item.icon}</span>
						{item.label}
					</a>
				{/each}
			</nav>

			<!-- User info / logout -->
			<div class="px-3 py-4 border-t border-slate-700">
				<div class="px-3 py-2 mb-2">
					<p class="text-sm font-medium text-slate-200">{data.admin?.name ?? data.admin?.email}</p>
					<p class="text-xs text-slate-500 capitalize">{data.admin?.role}</p>
				</div>
				<form method="POST" action="/logout">
					<button class="sidebar-link w-full text-left" type="submit">
						<span>🚪</span> Sign Out
					</button>
				</form>
			</div>
		</aside>

		<!-- Main content -->
		<main class="flex-1 overflow-y-auto">
			{@render children?.()}
		</main>
	</div>
{/if}
