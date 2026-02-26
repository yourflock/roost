<script lang="ts">
</script>

<svelte:head>
	<title>Subscription Active — Roost</title>
</svelte:head>

<div class="min-h-[80vh] flex items-center justify-center px-4">
	<div class="text-center max-w-lg">
		<div
			class="w-16 h-16 bg-green-500/20 border border-green-500/30 rounded-full flex items-center justify-center mx-auto mb-6"
		>
			<svg
				xmlns="http://www.w3.org/2000/svg"
				class="h-8 w-8 text-green-400"
				viewBox="0 0 20 20"
				fill="currentColor"
			>
				<path
					fill-rule="evenodd"
					d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
					clip-rule="evenodd"
				/>
			</svg>
		</div>
		<h1 class="text-3xl font-bold text-white mb-4">You're all set!</h1>
		<p class="text-slate-400 mb-8">
			Your Roost subscription is now active. Head to your dashboard to grab your API token and add
			Roost to Owl.
		</p>
		<a href="/dashboard" class="btn-primary text-base px-8 py-3"> Go to Dashboard </a>
	</div>
</div>
