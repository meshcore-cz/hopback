export interface FetchJsonOptions {
	/** Number of extra attempts after the first one. */
	retries?: number;
	/** Base delay before the first retry; doubles each subsequent attempt. */
	baseDelayMs?: number;
	/** Cap on the backoff delay. */
	maxDelayMs?: number;
	/** Log label used for retry warnings. */
	label?: string;
	verbose?: boolean;
}

/**
 * Fetches JSON with exponential backoff. Upstream node/observer APIs occasionally
 * drop the connection (ECONNRESET / "terminated"), which previously left a source
 * empty until the next refresh cycle; retrying smooths over those transient blips.
 */
export async function fetchJsonWithRetry(url: string, options: FetchJsonOptions = {}): Promise<unknown> {
	const { retries = 3, baseDelayMs = 1000, maxDelayMs = 15000, label = 'fetch', verbose = false } = options;

	let lastError: unknown;
	for (let attempt = 0; attempt <= retries; attempt++) {
		try {
			const response = await fetch(url);
			if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
			return await response.json();
		} catch (error) {
			lastError = error;
			if (attempt >= retries) break;
			const delay = Math.min(baseDelayMs * 2 ** attempt, maxDelayMs);
			if (verbose) {
				const reason = error instanceof Error ? error.message : String(error);
				console.warn(
					`[${label}] attempt ${attempt + 1}/${retries + 1} failed for ${url} (${reason}); retrying in ${delay}ms`
				);
			}
			await new Promise((resolve) => setTimeout(resolve, delay));
		}
	}
	throw lastError;
}
