// Roost API client — wraps all backend service calls

export interface ApiError {
	code: string;
	message: string;
	status: number;
}

export interface Subscriber {
	id: string;
	email: string;
	name: string;
	created_at: string;
	is_founder: boolean;
	flock_user_id?: string | null;
}

export interface Subscription {
	id: string;
	status: 'active' | 'cancelled' | 'suspended' | 'trialing' | 'past_due';
	plan: 'basic' | 'premium' | 'family';
	billing_period: 'monthly' | 'annual';
	current_period_end: string;
	cancel_at_period_end: boolean;
	stripe_subscription_id: string;
}

export interface ApiToken {
	token: string;
	created_at: string;
	last_used_at: string | null;
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

export interface Plan {
	id: string;
	name: string;
	description: string;
	price_monthly: number;
	price_annual: number;
	max_streams: number;
	features: string[];
	is_popular?: boolean;
}

export class RoostApiClient {
	private baseUrl: string;
	private sessionToken: string | null;

	constructor(baseUrl: string, sessionToken?: string | null) {
		this.baseUrl = baseUrl;
		this.sessionToken = sessionToken ?? null;
	}

	private async request<T>(
		method: string,
		path: string,
		body?: unknown
	): Promise<T> {
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
			let errBody: Partial<ApiError> = {};
			try {
				errBody = await res.json();
			} catch (_) {
				// ignore parse failure
			}
			throw {
				code: errBody.code ?? 'UNKNOWN',
				message: errBody.message ?? res.statusText,
				status: res.status
			} as ApiError;
		}

		const text = await res.text();
		return text ? JSON.parse(text) : ({} as T);
	}

	// Auth
	async login(email: string, password: string): Promise<{ session_token: string; subscriber: Subscriber }> {
		return this.request('POST', '/auth/login', { email, password });
	}

	async logout(): Promise<void> {
		return this.request('POST', '/auth/logout');
	}

	async getMe(): Promise<Subscriber> {
		return this.request('GET', '/auth/me');
	}

	async changePassword(currentPassword: string, newPassword: string): Promise<void> {
		return this.request('POST', '/auth/change-password', { current_password: currentPassword, new_password: newPassword });
	}

	async changeEmail(email: string, password: string): Promise<void> {
		return this.request('POST', '/auth/change-email', { email, password });
	}

	async deleteAccount(password: string): Promise<void> {
		return this.request('POST', '/auth/delete-account', { password });
	}

	// Subscription
	async getSubscription(): Promise<Subscription | null> {
		try {
			return await this.request<Subscription>('GET', '/billing/subscription');
		} catch (e: unknown) {
			const err = e as ApiError;
			if (err.status === 404) return null;
			throw e;
		}
	}

	// Plans
	async getPlans(): Promise<Plan[]> {
		return this.request('GET', '/billing/plans');
	}

	// Checkout
	async createCheckout(planId: string, billingPeriod: 'monthly' | 'annual'): Promise<{ checkout_url: string }> {
		return this.request('POST', '/billing/checkout', { plan_id: planId, billing_period: billingPeriod });
	}

	// Billing portal (Stripe)
	async getBillingPortalUrl(): Promise<{ url: string }> {
		return this.request('POST', '/billing/portal');
	}

	// Invoices
	async getInvoices(): Promise<Invoice[]> {
		return this.request('GET', '/billing/invoices');
	}

	// API Token
	async getApiToken(): Promise<ApiToken> {
		return this.request('GET', '/billing/api-token');
	}

	async regenerateApiToken(): Promise<ApiToken> {
		return this.request('POST', '/billing/api-token/regenerate');
	}

	// Cancel subscription
	async cancelSubscription(): Promise<void> {
		return this.request('POST', '/billing/cancel');
	}

	async resumeSubscription(): Promise<void> {
		return this.request('POST', '/billing/resume');
	}
}
