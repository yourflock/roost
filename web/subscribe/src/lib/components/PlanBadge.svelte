<script lang="ts">
	export let status: 'active' | 'cancelled' | 'suspended' | 'trialing' | 'past_due' | null = null;
	export let isFounder: boolean = false;
	export let plan: string | null = null;

	$: badgeClass = isFounder
		? 'badge-founder'
		: status === 'active' || status === 'trialing'
			? 'badge-active'
			: status === 'cancelled'
				? 'badge-cancelled'
				: status === 'suspended' || status === 'past_due'
					? 'badge-suspended'
					: 'badge-cancelled';

	$: label = isFounder
		? 'Founding Family ♾️'
		: status === 'active'
			? plan
				? `${capitalize(plan)} — Active`
				: 'Active'
			: status === 'trialing'
				? 'Trial'
				: status === 'cancelled'
					? 'Cancelled'
					: status === 'suspended'
						? 'Suspended'
						: status === 'past_due'
							? 'Past Due'
							: 'No Subscription';

	function capitalize(s: string): string {
		return s.charAt(0).toUpperCase() + s.slice(1);
	}
</script>

<span class={badgeClass}>{label}</span>
