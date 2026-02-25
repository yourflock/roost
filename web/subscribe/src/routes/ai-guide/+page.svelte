<script lang="ts">
	import { onMount } from 'svelte';

	interface Recommendation {
		id: string;
		content_id: string;
		content_type: string;
		score: number;
		reason: string;
		expires_at: string;
		created_at: string;
	}

	interface TrendingItem {
		content_id: string;
		content_type: string;
		avg_score: number;
		family_count: number;
	}

	type Tab = 'for-you' | 'trending';
	let activeTab = $state<Tab>('for-you');

	let recommendations = $state<Recommendation[]>([]);
	let trending = $state<TrendingItem[]>([]);
	let loadingRecs = $state(true);
	let loadingTrending = $state(false);
	let hint = $state('');
	let recsError = $state('');
	let trendingError = $state('');

	let refreshing = $state(false);
	let refreshJobID = $state('');

	let feedbackSent = $state<Record<string, string>>({});
	let sendingFeedback = $state<string | null>(null);

	async function loadRecommendations() {
		loadingRecs = true;
		recsError = '';
		hint = '';
		try {
			const res = await fetch('/api/ai-guide/recommendations');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const data = await res.json();
			recommendations = data.recommendations || [];
			hint = data.hint || '';
		} catch (e) {
			recsError = e instanceof Error ? e.message : 'Failed to load recommendations';
		} finally {
			loadingRecs = false;
		}
	}

	async function loadTrending() {
		loadingTrending = true;
		trendingError = '';
		try {
			const res = await fetch('/api/ai-guide/trending');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const data = await res.json();
			trending = data.trending || [];
		} catch (e) {
			trendingError = e instanceof Error ? e.message : 'Failed to load trending';
		} finally {
			loadingTrending = false;
		}
	}

	async function refreshRecommendations() {
		refreshing = true;
		refreshJobID = '';
		try {
			const res = await fetch('/api/ai-guide/recommendations/refresh', { method: 'POST' });
			if (res.ok) {
				const data = await res.json();
				refreshJobID = data.job_id || '';
				// Poll for results after a short delay
				setTimeout(async () => {
					await loadRecommendations();
				}, 3000);
			}
		} catch {
			// ignore
		} finally {
			refreshing = false;
		}
	}

	async function sendFeedback(contentID: string, feedback: 'like' | 'dislike' | 'not_interested' | 'already_seen') {
		sendingFeedback = contentID;
		try {
			const res = await fetch('/api/ai-guide/feedback', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ content_id: contentID, feedback })
			});
			if (res.ok) {
				feedbackSent[contentID] = feedback;
				if (feedback === 'dislike' || feedback === 'not_interested') {
					// Remove from list immediately
					recommendations = recommendations.filter((r) => r.content_id !== contentID);
				}
			}
		} catch {
			// ignore
		} finally {
			sendingFeedback = null;
		}
	}

	function contentTypeLabel(type: string): string {
		const labels: Record<string, string> = {
			movie: 'Movie',
			show: 'TV Show',
			show_episode: 'Episode',
			live: 'Live TV',
			podcast: 'Podcast',
			game: 'Game',
			music: 'Music'
		};
		return labels[type] || type;
	}

	function contentTypeColor(type: string): string {
		const colors: Record<string, string> = {
			movie: 'bg-blue-500/20 text-blue-300',
			show: 'bg-purple-500/20 text-purple-300',
			show_episode: 'bg-purple-500/20 text-purple-300',
			live: 'bg-red-500/20 text-red-300',
			podcast: 'bg-green-500/20 text-green-300',
			game: 'bg-yellow-500/20 text-yellow-300',
			music: 'bg-pink-500/20 text-pink-300'
		};
		return colors[type] || 'bg-slate-500/20 text-slate-300';
	}

	function scoreBar(score: number): string {
		const pct = Math.round(score * 100);
		return pct + '%';
	}

	function formatExpiry(expiresAt: string): string {
		const ms = new Date(expiresAt).getTime() - Date.now();
		if (ms <= 0) return 'Expired';
		const hrs = Math.floor(ms / 3600000);
		if (hrs > 0) return `Expires in ${hrs}h`;
		const mins = Math.floor(ms / 60000);
		return `Expires in ${mins}m`;
	}

	function switchTab(tab: Tab) {
		activeTab = tab;
		if (tab === 'trending' && trending.length === 0) {
			loadTrending();
		}
	}

	onMount(loadRecommendations);
</script>

<svelte:head>
	<title>AI Guide — Roost</title>
</svelte:head>

<div class="max-w-4xl mx-auto px-4 py-10">
	<div class="flex items-start justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-white">AI Guide</h1>
			<p class="text-slate-400 text-sm mt-1">Personalized recommendations powered by your watch history</p>
		</div>
		<button
			class="btn-secondary btn-sm"
			onclick={refreshRecommendations}
			disabled={refreshing}
		>
			{refreshing ? 'Generating…' : 'Refresh'}
		</button>
	</div>

	{#if refreshJobID}
		<div class="bg-blue-500/10 border border-blue-500/30 text-blue-300 text-sm px-4 py-3 rounded-lg mb-6">
			Generating new recommendations in the background. They will appear in a few seconds.
		</div>
	{/if}

	<!-- Tabs -->
	<div class="flex border-b border-slate-700 mb-6">
		<button
			class="px-4 py-2.5 text-sm font-medium transition-colors
				{activeTab === 'for-you'
					? 'text-white border-b-2 border-roost-500'
					: 'text-slate-400 hover:text-slate-300'}"
			onclick={() => switchTab('for-you')}
		>
			For You
		</button>
		<button
			class="px-4 py-2.5 text-sm font-medium transition-colors
				{activeTab === 'trending'
					? 'text-white border-b-2 border-roost-500'
					: 'text-slate-400 hover:text-slate-300'}"
			onclick={() => switchTab('trending')}
		>
			Trending
		</button>
	</div>

	<!-- For You tab -->
	{#if activeTab === 'for-you'}
		{#if recsError}
			<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6">
				{recsError}
			</div>
		{/if}

		{#if loadingRecs}
			<div class="card text-center py-12">
				<p class="text-slate-400">Loading recommendations…</p>
			</div>
		{:else if recommendations.length === 0}
			<div class="card text-center py-16">
				<div class="text-5xl mb-4">🤖</div>
				<h2 class="text-lg font-semibold text-white mb-2">No recommendations yet</h2>
				{#if hint}
					<p class="text-slate-400 text-sm mb-4">{hint}</p>
				{:else}
					<p class="text-slate-400 text-sm mb-4">
						Watch some content and your AI Guide will learn your preferences.
					</p>
				{/if}
				<button class="btn-primary" onclick={refreshRecommendations} disabled={refreshing}>
					{refreshing ? 'Generating…' : 'Generate Recommendations'}
				</button>
			</div>
		{:else}
			<div class="space-y-3">
				{#each recommendations as rec}
					{@const isSending = sendingFeedback === rec.content_id}
					{@const sentFeedback = feedbackSent[rec.content_id]}
					<div class="card hover:border-slate-600 transition-colors">
						<div class="flex items-start gap-4">
							<!-- Score indicator -->
							<div class="shrink-0 w-10 text-center pt-0.5">
								<div class="text-lg font-bold text-white">{Math.round(rec.score * 10)}</div>
								<div class="text-slate-500 text-xs">/10</div>
							</div>

							<div class="flex-1 min-w-0">
								<div class="flex items-start justify-between gap-2 mb-1.5">
									<div class="flex items-center gap-2 min-w-0">
										<code class="text-white font-medium text-sm truncate">{rec.content_id}</code>
										<span class="shrink-0 text-xs px-1.5 py-0.5 rounded-full {contentTypeColor(rec.content_type)}">
											{contentTypeLabel(rec.content_type)}
										</span>
									</div>
									<span class="shrink-0 text-xs text-slate-500">{formatExpiry(rec.expires_at)}</span>
								</div>

								{#if rec.reason}
									<p class="text-slate-400 text-sm mb-2">{rec.reason}</p>
								{/if}

								<!-- Score bar -->
								<div class="h-1 bg-slate-700 rounded-full mb-3 overflow-hidden">
									<div
										class="h-full bg-gradient-to-r from-roost-600 to-roost-400 rounded-full"
										style="width: {scoreBar(rec.score)}"
									></div>
								</div>

								<!-- Feedback row -->
								{#if sentFeedback}
									<p class="text-xs text-slate-500 capitalize">
										Marked as: <span class="text-slate-400">{sentFeedback.replace('_', ' ')}</span>
									</p>
								{:else}
									<div class="flex gap-2">
										<button
											class="text-xs text-slate-400 hover:text-green-400 transition-colors"
											onclick={() => sendFeedback(rec.content_id, 'like')}
											disabled={isSending}
										>
											Like
										</button>
										<span class="text-slate-600">·</span>
										<button
											class="text-xs text-slate-400 hover:text-red-400 transition-colors"
											onclick={() => sendFeedback(rec.content_id, 'dislike')}
											disabled={isSending}
										>
											Dislike
										</button>
										<span class="text-slate-600">·</span>
										<button
											class="text-xs text-slate-400 hover:text-slate-300 transition-colors"
											onclick={() => sendFeedback(rec.content_id, 'not_interested')}
											disabled={isSending}
										>
											Not interested
										</button>
										<span class="text-slate-600">·</span>
										<button
											class="text-xs text-slate-400 hover:text-slate-300 transition-colors"
											onclick={() => sendFeedback(rec.content_id, 'already_seen')}
											disabled={isSending}
										>
											Already seen
										</button>
									</div>
								{/if}
							</div>
						</div>
					</div>
				{/each}
			</div>

			<p class="text-xs text-slate-500 mt-6 text-center">
				{recommendations.length} recommendation{recommendations.length !== 1 ? 's' : ''} ·
				Feedback improves future suggestions
			</p>
		{/if}
	{/if}

	<!-- Trending tab -->
	{#if activeTab === 'trending'}
		{#if trendingError}
			<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6">
				{trendingError}
			</div>
		{/if}

		{#if loadingTrending}
			<div class="card text-center py-12">
				<p class="text-slate-400">Loading trending…</p>
			</div>
		{:else if trending.length === 0}
			<div class="card text-center py-12">
				<p class="text-slate-400">No trending data yet. Check back once more families are watching.</p>
			</div>
		{:else}
			<div class="space-y-2">
				{#each trending as item, i}
					<div class="card flex items-center gap-4 hover:border-slate-600 transition-colors">
						<span class="text-slate-500 text-sm font-mono w-5 shrink-0">#{i + 1}</span>
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2">
								<code class="text-white text-sm font-medium truncate">{item.content_id}</code>
								<span class="shrink-0 text-xs px-1.5 py-0.5 rounded-full {contentTypeColor(item.content_type)}">
									{contentTypeLabel(item.content_type)}
								</span>
							</div>
						</div>
						<div class="shrink-0 text-right">
							<p class="text-white text-sm font-medium">{(item.avg_score * 10).toFixed(1)}/10</p>
							<p class="text-slate-500 text-xs">{item.family_count} {item.family_count === 1 ? 'family' : 'families'}</p>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</div>
