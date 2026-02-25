<!-- +layout.svelte — Reseller portal layout (P14-T05) -->
<script lang="ts">
	import '../app.css';
	import type { LayoutData } from './$types';
	export let data: LayoutData;
</script>

<svelte:head>
	<title>Roost Reseller Portal</title>
</svelte:head>

<div class="min-h-screen bg-slate-900">
	{#if data.token}
		<!-- Authenticated layout with nav -->
		<div class="flex h-screen overflow-hidden">
			<!-- Sidebar -->
			<aside class="w-56 flex-shrink-0 border-r border-slate-800 bg-slate-900 flex flex-col">
				<div class="p-4 border-b border-slate-800">
					<div class="flex items-center gap-2">
						<div class="w-8 h-8 bg-roost-500 rounded-lg flex items-center justify-center text-white font-bold text-xs">R</div>
						<div>
							<p class="text-white font-semibold text-sm">Roost Reseller</p>
							<p class="text-slate-500 text-xs truncate">{data.resellerName ?? 'Partner'}</p>
						</div>
					</div>
				</div>

				<nav class="flex-1 p-3 space-y-1">
					<a href="/dashboard" class="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800 transition-colors">
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2H5a2 2 0 00-2-2z"></path></svg>
						Dashboard
					</a>
					<a href="/subscribers" class="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800 transition-colors">
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"></path></svg>
						Subscribers
					</a>
					<a href="/revenue" class="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-slate-400 hover:text-white hover:bg-slate-800 transition-colors">
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8c-1.657 0-3 .895-3 2s1.343 2 3 2 3 .895 3 2-1.343 2-3 2m0-8c1.11 0 2.08.402 2.599 1M12 8V7m0 1v8m0 0v1m0-1c-1.11 0-2.08-.402-2.599-1M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
						Revenue
					</a>
				</nav>

				<div class="p-3 border-t border-slate-800">
					<a href="/logout" class="flex items-center gap-2 px-3 py-2 rounded-lg text-sm text-slate-500 hover:text-slate-300 hover:bg-slate-800 transition-colors">
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"></path></svg>
						Sign Out
					</a>
				</div>
			</aside>

			<!-- Main content -->
			<main class="flex-1 overflow-auto p-6">
				<slot />
			</main>
		</div>
	{:else}
		<!-- Unauthenticated — just render the page (login page handles its own layout) -->
		<slot />
	{/if}
</div>
