<script lang="ts">
	import '../app.css';
	import type { LayoutData } from './$types';
	import { locale } from '$lib/i18n';

	export let data: LayoutData;

	$: subscriber = data.subscriber;
	$: isLoggedIn = !!subscriber;
	$: dir = $locale === 'ar' ? ('rtl' as const) : ('ltr' as const);

	const navLinks = [
		{ href: '/', label: 'Home' },
		{ href: '/plans', label: 'Plans' }
	];

	const authLinks = [
		{ href: '/dashboard', label: 'Dashboard' },
		{ href: '/dashboard/profiles', label: 'Profiles' },
		{ href: '/billing', label: 'Billing' },
		{ href: '/account', label: 'Account' }
	];
</script>

<div class="min-h-screen flex flex-col bg-slate-900" {dir}>
	<!-- Header -->
	<header class="border-b border-slate-800 bg-slate-900/95 backdrop-blur sticky top-0 z-50">
		<div class="max-w-5xl mx-auto px-4 h-16 flex items-center justify-between">
			<!-- Logo -->
			<a href="/" class="flex items-center gap-2 text-white font-semibold text-lg">
				<div
					class="w-8 h-8 bg-roost-500 rounded-lg flex items-center justify-center text-white font-bold text-sm"
				>
					R
				</div>
				<span>Roost</span>
			</a>

			<!-- Nav -->
			<nav class="hidden md:flex items-center gap-6 text-sm">
				{#each navLinks as link}
					<a href={link.href} class="text-slate-400 hover:text-white transition-colors"
						>{link.label}</a
					>
				{/each}
				{#if isLoggedIn}
					{#each authLinks as link}
						<a href={link.href} class="text-slate-400 hover:text-white transition-colors"
							>{link.label}</a
						>
					{/each}
					<form method="POST" action="/logout">
						<button class="text-slate-400 hover:text-white transition-colors">Sign Out</button>
					</form>
				{:else}
					<a href="/login" class="text-slate-400 hover:text-white transition-colors">Sign In</a>
					<a href="/subscribe" class="btn-primary text-sm">Subscribe</a>
				{/if}
			</nav>

			<!-- Mobile menu placeholder -->
			<div class="md:hidden">
				{#if isLoggedIn}
					<a href="/dashboard" class="btn-primary text-sm">Dashboard</a>
				{:else}
					<a href="/login" class="btn-secondary text-sm">Sign In</a>
				{/if}
			</div>
		</div>
	</header>

	<!-- Main content -->
	<main class="flex-1">
		<slot />
	</main>

	<!-- Footer -->
	<footer class="border-t border-slate-800 py-8 mt-16">
		<div class="max-w-5xl mx-auto px-4 text-center text-sm text-slate-500">
			<p>&copy; {new Date().getFullYear()} Roost by Flock. All rights reserved.</p>
			<div class="flex items-center justify-center gap-4 mt-2">
				<a
					href="https://owl.yourflock.org"
					class="hover:text-slate-300 transition-colors"
					target="_blank">Owl</a
				>
				<a
					href="https://yourflock.org"
					class="hover:text-slate-300 transition-colors"
					target="_blank">Flock</a
				>
			</div>
		</div>
	</footer>
</div>
