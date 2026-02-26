<script lang="ts">
	interface DarkContent {
		id: string;
		title: string;
		r2_path: string;
		created_at: string;
		invite_count?: number;
		viewer_count?: number;
	}

	interface GeneratedCode {
		code: string;
		redemption_url: string;
	}

	let contents: DarkContent[] = $state([]);
	let loading = $state(false);
	let errorMsg = $state('');

	// Upload modal state
	let showUpload = $state(false);
	let uploadTitle = $state('');
	let uploadFile: File | null = $state(null);
	let uploading = $state(false);
	let uploadError = $state('');

	// Invite code modal state
	let showInviteModal = $state(false);
	let selectedContent: DarkContent | null = $state(null);
	let inviteCount = $state(5);
	let generatingCodes = $state(false);
	let generatedCodes: GeneratedCode[] = $state([]);

	// Copied state
	let copiedCode = $state('');

	async function loadContents() {
		loading = true;
		try {
			const res = await fetch('/operator/v1/dark-vod');
			if (res.ok) {
				contents = await res.json();
			} else {
				errorMsg = 'Failed to load dark content list.';
			}
		} catch {
			errorMsg = 'Network error loading content.';
		} finally {
			loading = false;
		}
	}

	async function handleUpload() {
		if (!uploadTitle.trim() || !uploadFile) {
			uploadError = 'Title and file are required.';
			return;
		}
		uploading = true;
		uploadError = '';
		try {
			const form = new FormData();
			form.append('title', uploadTitle.trim());
			form.append('file', uploadFile);
			const res = await fetch('/operator/v1/dark-vod/upload', {
				method: 'POST',
				body: form
			});
			if (res.ok) {
				showUpload = false;
				uploadTitle = '';
				uploadFile = null;
				await loadContents();
			} else {
				const err = await res.text();
				uploadError = err || 'Upload failed.';
			}
		} catch {
			uploadError = 'Network error during upload.';
		} finally {
			uploading = false;
		}
	}

	function openInviteModal(content: DarkContent) {
		selectedContent = content;
		generatedCodes = [];
		inviteCount = 5;
		showInviteModal = true;
	}

	async function generateInviteCodes() {
		if (!selectedContent) return;
		generatingCodes = true;
		try {
			const res = await fetch(`/operator/v1/dark-vod/${selectedContent.id}/invite-codes`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ count: inviteCount })
			});
			if (res.ok) {
				generatedCodes = await res.json();
			}
		} finally {
			generatingCodes = false;
		}
	}

	function copyToClipboard(text: string, key: string) {
		navigator.clipboard.writeText(text);
		copiedCode = key;
		setTimeout(() => {
			copiedCode = '';
		}, 2000);
	}

	function formatDate(iso: string) {
		return new Date(iso).toLocaleDateString('en-US', {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	$effect(() => {
		loadContents();
	});
</script>

<svelte:head>
	<title>Dark VOD — Roost Admin</title>
</svelte:head>

<div class="max-w-5xl mx-auto px-4 py-10">
	<div class="flex items-center justify-between mb-8">
		<div>
			<h1 class="text-2xl font-bold text-white">Dark VOD</h1>
			<p class="text-slate-400 text-sm mt-1">
				Private content distribution via invite codes. Content is stored in R2 and access is
				controlled by signed viewer tokens. Recipients do not need a Roost account.
			</p>
		</div>
		<button
			class="btn-primary"
			onclick={() => {
				showUpload = true;
			}}
		>
			Upload Dark Content
		</button>
	</div>

	{#if errorMsg}
		<div
			class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm mb-6"
		>
			{errorMsg}
		</div>
	{/if}

	{#if loading}
		<div class="text-slate-400 text-sm">Loading content...</div>
	{:else if contents.length === 0}
		<div class="card text-center py-16">
			<p class="text-slate-400 text-sm">No dark content yet.</p>
			<p class="text-slate-500 text-xs mt-1">
				Upload content to generate invite codes for private distribution.
			</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each contents as content (content.id)}
				<div class="card flex items-center justify-between gap-4">
					<div class="min-w-0">
						<p class="text-white font-medium truncate">{content.title}</p>
						<p class="text-slate-500 text-xs mt-0.5">
							Uploaded {formatDate(content.created_at)}
							{#if content.invite_count !== undefined}
								&middot; {content.invite_count} codes issued
							{/if}
							{#if content.viewer_count !== undefined}
								&middot; {content.viewer_count} viewers
							{/if}
						</p>
						<p class="text-slate-600 text-xs mt-0.5 font-mono truncate">{content.r2_path}</p>
					</div>
					<button class="btn-secondary text-sm shrink-0" onclick={() => openInviteModal(content)}>
						Generate Invite Codes
					</button>
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Upload Modal -->
{#if showUpload}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl w-full max-w-md p-6">
			<h2 class="text-lg font-semibold text-white mb-4">Upload Dark Content</h2>

			{#if uploadError}
				<div
					class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-4"
				>
					{uploadError}
				</div>
			{/if}

			<div class="space-y-4">
				<div>
					<label for="dark-title" class="label">Title</label>
					<input
						id="dark-title"
						type="text"
						bind:value={uploadTitle}
						class="input"
						placeholder="Content title"
					/>
				</div>
				<div>
					<label for="dark-file" class="label">File</label>
					<input
						id="dark-file"
						type="file"
						accept=".mp4,.mkv,.avi,.m4v,.ts"
						onchange={(e) => {
							const t = e.currentTarget as HTMLInputElement;
							uploadFile = t.files?.[0] ?? null;
						}}
						class="block w-full text-sm text-slate-400 file:mr-3 file:py-1.5 file:px-3 file:rounded file:border-0 file:bg-slate-700 file:text-slate-200 file:text-sm hover:file:bg-slate-600 cursor-pointer"
					/>
				</div>
				<p class="text-slate-500 text-xs">
					Content is stored in R2 at <code class="font-mono">roost-vod/dark/</code>. Only recipients
					with a valid invite code can access it.
				</p>
			</div>

			<div class="flex gap-3 mt-6">
				<button class="btn-primary flex-1" onclick={handleUpload} disabled={uploading}>
					{uploading ? 'Uploading...' : 'Upload'}
				</button>
				<button
					class="btn-secondary flex-1"
					onclick={() => {
						showUpload = false;
						uploadError = '';
					}}
					disabled={uploading}
				>
					Cancel
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Invite Code Modal -->
{#if showInviteModal && selectedContent}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
		<div
			class="bg-slate-800 border border-slate-700 rounded-xl w-full max-w-lg p-6 max-h-[90vh] overflow-y-auto"
		>
			<h2 class="text-lg font-semibold text-white mb-1">Generate Invite Codes</h2>
			<p class="text-slate-400 text-sm mb-4">{selectedContent.title}</p>

			<div class="flex items-center gap-3 mb-4">
				<div class="flex-1">
					<label for="invite-count" class="label">Number of codes</label>
					<select id="invite-count" bind:value={inviteCount} class="input">
						<option value={1}>1</option>
						<option value={5}>5</option>
						<option value={10}>10</option>
						<option value={25}>25</option>
						<option value={50}>50</option>
						<option value={100}>100</option>
					</select>
				</div>
				<div class="pt-5">
					<button class="btn-primary" onclick={generateInviteCodes} disabled={generatingCodes}>
						{generatingCodes ? 'Generating...' : 'Generate'}
					</button>
				</div>
			</div>

			{#if generatedCodes.length > 0}
				<div class="border-t border-slate-700 pt-4 space-y-2">
					<p class="text-slate-300 text-sm font-medium mb-2">
						{generatedCodes.length} codes generated — save these now, they are shown once.
					</p>
					{#each generatedCodes as codeEntry (codeEntry.code)}
						<div class="bg-slate-900 rounded-lg px-3 py-2 flex items-center justify-between gap-3">
							<div class="min-w-0">
								<p class="font-mono text-xs text-indigo-300 truncate">{codeEntry.code}</p>
								<p class="text-slate-500 text-xs truncate">{codeEntry.redemption_url}</p>
							</div>
							<button
								class="text-xs text-slate-400 hover:text-white shrink-0 transition-colors"
								onclick={() => copyToClipboard(codeEntry.redemption_url, codeEntry.code)}
							>
								{copiedCode === codeEntry.code ? 'Copied!' : 'Copy URL'}
							</button>
						</div>
					{/each}
				</div>
			{/if}

			<div class="mt-6">
				<button
					class="btn-secondary w-full"
					onclick={() => {
						showInviteModal = false;
						generatedCodes = [];
					}}
				>
					Close
				</button>
			</div>
		</div>
	</div>
{/if}
