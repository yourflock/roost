<script lang="ts">
	import type { PageData } from './$types';
	import type { Plan } from '$lib/api';
	export let data: PageData;

	let annual = false;

	// P14-T03: Regional price type extending Plan
	interface RegionalPrice {
		plan_id: string;
		region_id: string;
		region_code: string;
		region_name: string;
		currency: string;
		monthly_price_cents: number;
		annual_price_cents: number;
	}
	type PlanWithRegion = Plan & { regional_price?: RegionalPrice };

	function formatCurrency(cents: number, currencyCode: string): string {
		try {
			return new Intl.NumberFormat(undefined, {
				style: 'currency',
				currency: currencyCode,
				minimumFractionDigits: 2,
				maximumFractionDigits: 2
			}).format(cents / 100);
		} catch {
			return `${(cents / 100).toFixed(2)} ${currencyCode}`;
		}
	}

	function monthlyPrice(plan: Plan): string {
		const rp = (plan as PlanWithRegion).regional_price;
		const currency = rp?.currency ?? 'usd';
		const cents = annual
			? Math.round((rp?.annual_price_cents ?? plan.price_annual) / 12)
			: (rp?.monthly_price_cents ?? plan.price_monthly);
		return formatCurrency(cents, currency.toUpperCase());
	}

	function annualTotal(plan: Plan): string {
		const rp = (plan as PlanWithRegion).regional_price;
		const currency = rp?.currency ?? 'usd';
		const cents = rp?.annual_price_cents ?? plan.price_annual;
		return formatCurrency(cents, currency.toUpperCase());
	}

	function savings(plan: Plan): number {
		const rp = (plan as PlanWithRegion).regional_price;
		const monthly = rp?.monthly_price_cents ?? plan.price_monthly;
		const annual = rp?.annual_price_cents ?? plan.price_annual;
		return monthly * 12 - annual;
	}
</script>

<svelte:head>
	<title>Plans — Roost</title>
</svelte:head>

<div class="max-w-5xl mx-auto px-4 py-16">
	<div class="text-center mb-12">
		<h1 class="text-3xl font-bold text-white mb-4">Choose Your Plan</h1>
		<p class="text-slate-400 mb-8">
			All plans include the full channel lineup. Upgrade or cancel anytime.
		</p>

		<!-- Billing toggle -->
		<div class="inline-flex items-center gap-3 bg-slate-800 rounded-full p-1.5">
			<button
				on:click={() => (annual = false)}
				class="px-4 py-1.5 rounded-full text-sm font-medium transition-colors {!annual
					? 'bg-roost-500 text-white'
					: 'text-slate-400 hover:text-white'}"
			>
				Monthly
			</button>
			<button
				on:click={() => (annual = true)}
				class="px-4 py-1.5 rounded-full text-sm font-medium transition-colors {annual
					? 'bg-roost-500 text-white'
					: 'text-slate-400 hover:text-white'}"
			>
				Annual
				<span class="ml-1 text-xs {annual ? 'text-roost-200' : 'text-green-400'}"
					>Save 2 months</span
				>
			</button>
		</div>
	</div>

	<div class="grid grid-cols-1 md:grid-cols-3 gap-6">
		{#each data.plans as plan}
			<div
				class="card relative flex flex-col {plan.is_popular
					? 'border-roost-500 ring-1 ring-roost-500/50'
					: ''}"
			>
				{#if plan.is_popular}
					<div class="absolute -top-3.5 left-1/2 -translate-x-1/2">
						<span class="bg-roost-500 text-white text-xs font-semibold px-4 py-1 rounded-full"
							>Most Popular</span
						>
					</div>
				{/if}

				<div class="flex-1">
					<h2 class="text-xl font-semibold text-white mb-1">{plan.name}</h2>
					<p class="text-slate-400 text-sm mb-4">{plan.description}</p>

					<div class="mb-2">
						<span class="text-4xl font-bold text-white">${monthlyPrice(plan)}</span>
						<span class="text-slate-400 text-sm">/mo</span>
					</div>
					{#if annual}
						<p class="text-xs text-slate-500 mb-1">Billed ${annualTotal(plan)}/year</p>
						<p class="text-xs text-green-400 mb-4">
							Save ${(savings(plan) / 100).toFixed(2)} vs monthly
						</p>
					{:else}
						<p class="text-xs text-slate-500 mb-4">Billed monthly — cancel anytime</p>
					{/if}

					<ul class="space-y-2 mb-6">
						{#each plan.features as feature}
							<li class="flex items-start gap-2 text-sm text-slate-300">
								<svg
									xmlns="http://www.w3.org/2000/svg"
									class="h-4 w-4 text-roost-400 mt-0.5 flex-shrink-0"
									viewBox="0 0 20 20"
									fill="currentColor"
								>
									<path
										fill-rule="evenodd"
										d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
										clip-rule="evenodd"
									/>
								</svg>
								{feature}
							</li>
						{/each}
					</ul>
				</div>

				<a
					href="/subscribe?plan={plan.id}&period={annual ? 'annual' : 'monthly'}"
					class="{plan.is_popular ? 'btn-primary' : 'btn-secondary'} block text-center"
				>
					Get {plan.name}
				</a>
			</div>
		{/each}
	</div>

	<!-- FAQ / notes -->
	<div class="mt-12 text-center text-sm text-slate-500 space-y-1">
		<p>All plans are billed by Stripe. Your payment info is never stored on our servers.</p>
		<p>Downgrade, upgrade, or cancel anytime from your billing dashboard.</p>
	</div>
</div>
