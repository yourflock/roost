<script lang="ts">
	import type { PageData } from './$types';
	import type { Plan } from '$lib/api';
	export let data: PageData;

	let selectedPlanId = data.selectedPlan;
	let billingPeriod: 'monthly' | 'annual' = data.selectedPeriod;
	let loading = false;
	let error = '';

	// Step tracking: 1=select plan, 2=account, 3=checkout
	let step = 1;

	// Account creation fields
	let name = '';
	let email = '';
	let password = '';
	let passwordConfirm = '';
	let selectedRegion = ''; // P14-T06: region selector

	$: selectedPlan = data.plans.find(p => p.id === selectedPlanId) ?? data.plans[1];

	function price(plan: Plan): string {
		const cents = billingPeriod === 'annual' ? Math.round(plan.price_annual / 12) : plan.price_monthly;
		return (cents / 100).toFixed(2);
	}

	async function handleCheckout() {
		if (password !== passwordConfirm) {
			error = 'Passwords do not match.';
			return;
		}
		if (password.length < 8) {
			error = 'Password must be at least 8 characters.';
			return;
		}
		loading = true;
		error = '';

		try {
			// Step 1: Create account
			const registerRes = await fetch('/api/subscribe/register', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ name, email, password, plan_id: selectedPlanId, billing_period: billingPeriod })
			});
			const registerData = await registerRes.json();
			if (!registerRes.ok) {
				error = registerData.message ?? 'Registration failed.';
				loading = false;
				return;
			}
			// Redirect to Stripe Checkout
			window.location.href = registerData.checkout_url;
		} catch (e: unknown) {
			error = 'Service unavailable. Please try again.';
			loading = false;
		}
	}
</script>

<svelte:head>
	<title>Subscribe — Roost</title>
</svelte:head>

<div class="max-w-2xl mx-auto px-4 py-16">
	<div class="text-center mb-8">
		<h1 class="text-2xl font-bold text-white">Start Watching</h1>
		<p class="text-slate-400 mt-1 text-sm">Cancel anytime. No hidden fees.</p>
	</div>

	{#if step === 1}
		<!-- Plan selection -->
		<div class="card mb-6">
			<h2 class="font-semibold text-white mb-4">Choose a Plan</h2>

			<!-- Billing period -->
			<div class="flex gap-2 mb-4">
				{#each (['monthly', 'annual'] as const) as period}
					<button
						on:click={() => (billingPeriod = period)}
						class="flex-1 py-2 rounded-lg text-sm font-medium border transition-colors {billingPeriod === period ? 'bg-roost-500 border-roost-500 text-white' : 'border-slate-600 text-slate-400 hover:border-slate-500'}"
					>
						{period === 'annual' ? 'Annual (save 2 months)' : 'Monthly'}
					</button>
				{/each}
			</div>

			<div class="space-y-3">
				{#each data.plans as plan}
					<label class="flex items-center gap-4 cursor-pointer">
						<input
							type="radio"
							name="plan"
							value={plan.id}
							bind:group={selectedPlanId}
							class="accent-roost-500 w-4 h-4"
						/>
						<div class="flex-1 flex items-center justify-between bg-slate-900 rounded-lg px-4 py-3 {selectedPlanId === plan.id ? 'ring-1 ring-roost-500' : ''}">
							<div>
								<span class="font-medium text-white">{plan.name}</span>
								<span class="text-slate-400 text-xs ml-2">{plan.max_streams} streams</span>
								{#if plan.is_popular}
									<span class="ml-2 text-xs text-roost-400">Popular</span>
								{/if}
							</div>
							<span class="font-semibold text-white">${price(plan)}/mo</span>
						</div>
					</label>
				{/each}
			</div>
		</div>

		<button on:click={() => (step = 2)} class="btn-primary w-full py-3">
			Continue with {selectedPlan?.name ?? 'Selected'} Plan
		</button>
		<p class="text-center text-sm text-slate-400 mt-3">
			Already have an account? <a href="/login" class="text-roost-400 hover:text-roost-300 underline">Sign in</a>
		</p>

	{:else if step === 2}
		<!-- Account creation -->
		<div class="card mb-6">
			<div class="flex items-center gap-3 mb-4">
				<button on:click={() => (step = 1)} class="text-slate-400 hover:text-white" aria-label="Go back to plan selection">
					<svg xmlns="http://www.w3.org/2000/svg" class="h-5 w-5" viewBox="0 0 20 20" fill="currentColor">
						<path fill-rule="evenodd" d="M9.707 16.707a1 1 0 01-1.414 0l-6-6a1 1 0 010-1.414l6-6a1 1 0 011.414 1.414L5.414 9H17a1 1 0 110 2H5.414l4.293 4.293a1 1 0 010 1.414z" clip-rule="evenodd" />
					</svg>
				</button>
				<h2 class="font-semibold text-white">Create Your Account</h2>
			</div>

			{#if error}
				<div class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm mb-4">
					{error}
				</div>
			{/if}

			<div class="space-y-4">
				<div>
					<label for="name" class="label">Full Name</label>
					<input id="name" bind:value={name} type="text" autocomplete="name" required class="input" placeholder="Your name" />
				</div>
				<div>
					<label for="email" class="label">Email</label>
					<input id="email" bind:value={email} type="email" autocomplete="email" required class="input" placeholder="you@example.com" />
				</div>
				<div>
					<label for="password" class="label">Password</label>
					<input id="password" bind:value={password} type="password" autocomplete="new-password" required minlength="8" class="input" placeholder="At least 8 characters" />
				</div>
				<div>
					<label for="password-confirm" class="label">Confirm Password</label>
					<input id="password-confirm" bind:value={passwordConfirm} type="password" autocomplete="new-password" required class="input" placeholder="Repeat password" />
				</div>
				<!-- Region selector (P14-T02/T06) -->
				<div>
					<label for="region" class="label">Region <span class="text-slate-500 font-normal">(optional)</span></label>
					<select id="region" bind:value={selectedRegion} class="select">
						<option value="">Select your region</option>
						<option value="us">🌎 North America</option>
						<option value="eu">🌍 Europe</option>
						<option value="mena">🌍 Middle East & North Africa</option>
						<option value="apac">🌏 Asia-Pacific</option>
						<option value="latam">🌎 Latin America</option>
					</select>
					<p class="text-xs text-slate-500 mt-1">Determines which channels are available to you.</p>
				</div>
			</div>
		</div>

		<!-- Order summary -->
		<div class="card mb-6 bg-slate-900">
			<h3 class="font-medium text-slate-300 mb-3">Order Summary</h3>
			<div class="flex justify-between text-sm mb-1">
				<span class="text-slate-400">{selectedPlan?.name} — {billingPeriod}</span>
				<span class="text-white">${
					selectedPlan
						? billingPeriod === 'annual'
							? (selectedPlan.price_annual / 100).toFixed(2)
							: (selectedPlan.price_monthly / 100).toFixed(2)
						: '—'
				}</span>
			</div>
			<div class="text-xs text-slate-500">
				{billingPeriod === 'annual' ? 'Billed once per year' : 'Billed monthly'}
			</div>
		</div>

		<button
			on:click={handleCheckout}
			disabled={loading || !name || !email || !password || !passwordConfirm}
			class="btn-primary w-full py-3 disabled:opacity-50 disabled:cursor-not-allowed"
		>
			{loading ? 'Redirecting to payment...' : 'Continue to Payment'}
		</button>
		<p class="text-center text-xs text-slate-500 mt-3">
			Secure payment via Stripe. We never store your card details.
		</p>
	{/if}
</div>
