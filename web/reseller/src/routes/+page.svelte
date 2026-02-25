<!-- Root page — redirect to dashboard if logged in, else to login -->
<script lang="ts">
	import { goto } from '$app/navigation';
	import type { PageData } from './$types';
	export let data: PageData;

	$: {
		if (data.token) {
			goto('/dashboard');
		} else {
			goto('/login');
		}
	}
</script>
