// Roost Admin API client

export interface AdminUser {
	id: string;
	email: string;
	name: string;
	role: 'superowner' | 'staff';
	is_superowner: boolean;
}

export interface Subscriber {
	id: string;
	email: string;
	name: string;
	created_at: string;
	is_founder: boolean;
	subscription: Subscription | null;
	stream_count: number;
}

export interface Subscription {
	id: string;
	status: 'active' | 'cancelled' | 'suspended' | 'trialing' | 'past_due';
	plan: 'basic' | 'premium' | 'family';
	billing_period: 'monthly' | 'annual';
	current_period_end: string;
	cancel_at_period_end: boolean;
	stripe_subscription_id: string;
	stripe_customer_id: string;
}

export interface Invoice {
	id: string;
	amount: number;
	currency: string;
	status: 'paid' | 'open' | 'void' | 'uncollectible';
	created_at: string;
	period_start: string;
	period_end: string;
	pdf_url: string | null;
}

export interface ApiToken {
	token: string;
	created_at: string;
	last_used_at: string | null;
}

export interface Channel {
	id: string;
	name: string;
	slug: string;
	category: string;
	stream_url: string;
	logo_url: string | null;
	epg_id: string | null;
	is_active: boolean;
	sort_order: number;
	created_at: string;
}

export interface ActiveStream {
	id: string;
	subscriber_id: string;
	subscriber_email: string;
	channel_id: string;
	channel_name: string;
	started_at: string;
	quality: '1080p' | '720p' | '480p' | '360p';
	bitrate_kbps: number;
	user_agent: string;
}

export interface EpgSource {
	id: string;
	name: string;
	url: string;
	format: 'xmltv' | 'm3u';
	is_active: boolean;
	last_synced_at: string | null;
	sync_status: 'idle' | 'syncing' | 'error' | 'success';
	error_message: string | null;
	channel_count: number;
}

export interface ServiceHealth {
	name: string;
	status: 'healthy' | 'degraded' | 'down';
	latency_ms: number | null;
	details: string | null;
	checked_at: string;
}

export interface DashboardStats {
	total_subscribers: number;
	active_subscribers: number;
	active_streams: number;
	total_channels: number;
	mrr_cents: number;
	arr_cents: number;
	new_subscribers_7d: number;
	churn_7d: number;
}

export interface PromoCode {
	id: string;
	code: string;
	discount_type: 'percent' | 'fixed';
	discount_value: number;
	max_uses: number | null;
	used_count: number;
	expires_at: string | null;
	is_active: boolean;
}

export interface ApiError {
	code: string;
	message: string;
	status: number;
}

export class AdminApiClient {
	private baseUrl: string;
	private sessionToken: string | null;

	constructor(baseUrl: string, sessionToken?: string | null) {
		this.baseUrl = baseUrl;
		this.sessionToken = sessionToken ?? null;
	}

	private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
		const headers: Record<string, string> = {
			'Content-Type': 'application/json'
		};
		if (this.sessionToken) {
			headers['Authorization'] = `Bearer ${this.sessionToken}`;
		}

		const res = await fetch(`${this.baseUrl}${path}`, {
			method,
			headers,
			body: body ? JSON.stringify(body) : undefined
		});

		if (!res.ok) {
			let error: ApiError;
			try {
				error = await res.json();
			} catch {
				error = { code: 'UNKNOWN', message: res.statusText, status: res.status };
			}
			throw error;
		}

		if (res.status === 204) return undefined as T;
		return res.json();
	}

	// Auth
	async login(email: string, password: string): Promise<{ token: string; admin: AdminUser }> {
		return this.request('POST', '/auth/admin/login', { email, password });
	}

	async logout(): Promise<void> {
		return this.request('POST', '/auth/admin/logout');
	}

	// Dashboard
	async getDashboardStats(): Promise<DashboardStats> {
		return this.request('GET', '/billing/admin/dashboard');
	}

	// Subscribers
	async getSubscribers(params?: {
		search?: string;
		plan?: string;
		status?: string;
		page?: number;
		per_page?: number;
	}): Promise<{ subscribers: Subscriber[]; total: number; page: number; per_page: number }> {
		const qs = new URLSearchParams();
		if (params?.search) qs.set('search', params.search);
		if (params?.plan) qs.set('plan', params.plan);
		if (params?.status) qs.set('status', params.status);
		if (params?.page) qs.set('page', String(params.page));
		if (params?.per_page) qs.set('per_page', String(params.per_page));
		const query = qs.toString() ? `?${qs.toString()}` : '';
		return this.request('GET', `/billing/admin/subscribers${query}`);
	}

	async getSubscriber(
		id: string
	): Promise<{ subscriber: Subscriber; invoices: Invoice[]; tokens: ApiToken[] }> {
		return this.request('GET', `/billing/admin/subscribers/${id}`);
	}

	async suspendSubscriber(id: string, reason: string): Promise<void> {
		return this.request('POST', `/billing/admin/subscribers/${id}/suspend`, { reason });
	}

	async reinstateSubscriber(id: string): Promise<void> {
		return this.request('POST', `/billing/admin/subscribers/${id}/reinstate`);
	}

	// Channels
	async getChannels(): Promise<Channel[]> {
		return this.request('GET', '/catalog/admin/channels');
	}

	async createChannel(data: Omit<Channel, 'id' | 'created_at'>): Promise<Channel> {
		return this.request('POST', '/catalog/admin/channels', data);
	}

	async updateChannel(id: string, data: Partial<Channel>): Promise<Channel> {
		return this.request('PUT', `/catalog/admin/channels/${id}`, data);
	}

	async deleteChannel(id: string): Promise<void> {
		return this.request('DELETE', `/catalog/admin/channels/${id}`);
	}

	async reorderChannels(ids: string[]): Promise<void> {
		return this.request('POST', '/catalog/admin/channels/reorder', { ids });
	}

	// Streams
	async getActiveStreams(): Promise<ActiveStream[]> {
		return this.request('GET', '/ingest/channels/health');
	}

	async terminateStream(streamId: string): Promise<void> {
		return this.request('DELETE', `/ingest/streams/${streamId}`);
	}

	// EPG
	async getEpgSources(): Promise<EpgSource[]> {
		return this.request('GET', '/epg/sources');
	}

	async addEpgSource(data: { name: string; url: string; format: 'xmltv' | 'm3u' }): Promise<EpgSource> {
		return this.request('POST', '/epg/sources', data);
	}

	async removeEpgSource(id: string): Promise<void> {
		return this.request('DELETE', `/epg/sources/${id}`);
	}

	async triggerEpgSync(id: string): Promise<void> {
		return this.request('POST', `/epg/sources/${id}/sync`);
	}

	// Billing / Promo codes
	async getPromoCodes(): Promise<PromoCode[]> {
		return this.request('GET', '/billing/admin/promo-codes');
	}

	async createPromoCode(
		data: Omit<PromoCode, 'id' | 'used_count'>
	): Promise<PromoCode> {
		return this.request('POST', '/billing/admin/promo-codes', data);
	}

	async deactivatePromoCode(id: string): Promise<void> {
		return this.request('DELETE', `/billing/admin/promo-codes/${id}`);
	}

	// System
	async getServiceHealth(): Promise<ServiceHealth[]> {
		return this.request('GET', '/system/health');
	}
}
