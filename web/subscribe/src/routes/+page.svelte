<script lang="ts">
	const features = [
		{ icon: '📺', title: 'Live TV', desc: '200+ channels — sports, news, entertainment, local.' },
		{ icon: '🎬', title: 'VOD', desc: 'Movies and shows on demand. No extra app needed.' },
		{
			icon: '📅',
			title: 'Full EPG',
			desc: 'Two-week electronic program guide. Never miss a game.'
		},
		{
			icon: '🎮',
			title: 'Sports',
			desc: 'Live sports with commercial detection and stats overlays.'
		},
		{
			icon: '🔒',
			title: 'Private',
			desc: 'Your viewing history stays yours. No ads, no profiling.'
		},
		{ icon: '🦉', title: 'Owl-first', desc: 'Purpose-built for Owl. Works in TiviMate, VLC, too.' }
	];

	const plans = [
		{ name: 'Basic', price: 5.99, streams: 2, popular: false },
		{ name: 'Premium', price: 9.99, streams: 4, popular: true },
		{ name: 'Family', price: 14.99, streams: 6, popular: false }
	];
</script>

<svelte:head>
	<title>Roost — Live TV for Owl</title>
	<meta
		name="description"
		content="Roost adds live TV, sports, and VOD to your Owl media player. Subscribe and start watching in minutes."
	/>
</svelte:head>

<!-- Hero -->
<section class="py-20 px-4 text-center">
	<div class="max-w-3xl mx-auto">
		<div
			class="inline-flex items-center gap-2 bg-roost-500/10 border border-roost-500/20 rounded-full px-4 py-1.5 text-sm text-roost-400 mb-6"
		>
			<span class="w-2 h-2 bg-roost-500 rounded-full animate-pulse"></span>
			200+ channels live now
		</div>
		<h1 class="text-5xl font-bold text-white mb-6 leading-tight">
			Live TV for <span class="text-roost-400">Owl</span>
		</h1>
		<p class="text-xl text-slate-400 mb-8 leading-relaxed">
			Add Roost to your Owl player and get live channels, sports, and on-demand content. One token.
			Instant activation. No new app.
		</p>
		<div class="flex items-center justify-center gap-4">
			<a href="/subscribe" class="btn-primary text-base px-8 py-3">Start Watching — $5.99/mo</a>
			<a href="/plans" class="btn-secondary text-base px-8 py-3">See All Plans</a>
		</div>
		<p class="text-sm text-slate-500 mt-4">No long-term contracts. Cancel anytime.</p>
	</div>
</section>

<!-- Features -->
<section class="py-16 px-4 border-t border-slate-800">
	<div class="max-w-5xl mx-auto">
		<h2 class="text-2xl font-bold text-white text-center mb-12">Everything you need</h2>
		<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
			{#each features as f}
				<div class="card">
					<div class="text-3xl mb-3">{f.icon}</div>
					<h3 class="font-semibold text-white mb-1">{f.title}</h3>
					<p class="text-sm text-slate-400">{f.desc}</p>
				</div>
			{/each}
		</div>
	</div>
</section>

<!-- How it works -->
<section class="py-16 px-4 border-t border-slate-800">
	<div class="max-w-3xl mx-auto text-center">
		<h2 class="text-2xl font-bold text-white mb-12">Set up in 3 steps</h2>
		<div class="grid grid-cols-1 md:grid-cols-3 gap-8">
			{#each [{ step: '1', title: 'Subscribe', desc: "Pick a plan. We'll generate your API token instantly." }, { step: '2', title: 'Add to Owl', desc: 'Open Owl → Settings → Community Addons → paste your token.' }, { step: '3', title: 'Watch', desc: 'Live channels, sports, and VOD appear in your Owl library.' }] as item}
				<div class="flex flex-col items-center">
					<div
						class="w-12 h-12 bg-roost-500 rounded-full flex items-center justify-center text-white font-bold text-lg mb-4"
					>
						{item.step}
					</div>
					<h3 class="font-semibold text-white mb-2">{item.title}</h3>
					<p class="text-sm text-slate-400">{item.desc}</p>
				</div>
			{/each}
		</div>
	</div>
</section>

<!-- Pricing preview -->
<section class="py-16 px-4 border-t border-slate-800">
	<div class="max-w-4xl mx-auto">
		<h2 class="text-2xl font-bold text-white text-center mb-4">Simple pricing</h2>
		<p class="text-slate-400 text-center mb-12">
			Pay monthly or save 2 months with annual billing.
		</p>
		<div class="grid grid-cols-1 md:grid-cols-3 gap-6">
			{#each plans as plan}
				<div class="card relative {plan.popular ? 'border-roost-500' : ''}">
					{#if plan.popular}
						<div class="absolute -top-3 left-1/2 -translate-x-1/2">
							<span class="bg-roost-500 text-white text-xs font-medium px-3 py-1 rounded-full"
								>Most Popular</span
							>
						</div>
					{/if}
					<h3 class="font-semibold text-white text-lg mb-1">{plan.name}</h3>
					<div class="text-3xl font-bold text-white mb-1">
						${plan.price}<span class="text-base font-normal text-slate-400">/mo</span>
					</div>
					<p class="text-sm text-slate-400 mb-4">Up to {plan.streams} concurrent streams</p>
					<a
						href="/subscribe?plan={plan.name.toLowerCase()}"
						class="{plan.popular ? 'btn-primary' : 'btn-secondary'} block text-center text-sm"
					>
						Get {plan.name}
					</a>
				</div>
			{/each}
		</div>
		<p class="text-center mt-6">
			<a href="/plans" class="text-roost-400 hover:text-roost-300 text-sm underline"
				>See full feature comparison →</a
			>
		</p>
	</div>
</section>

<!-- CTA -->
<section class="py-16 px-4 border-t border-slate-800">
	<div class="max-w-2xl mx-auto text-center">
		<h2 class="text-2xl font-bold text-white mb-4">Ready to watch?</h2>
		<p class="text-slate-400 mb-8">Join thousands of Owl users with live TV.</p>
		<a href="/subscribe" class="btn-primary text-base px-8 py-3">Subscribe Now</a>
	</div>
</section>
