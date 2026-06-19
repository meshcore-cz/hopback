/**
 * Parsing for an endpoint's `operator` contact string, e.g.
 * `Jan Novák <telegram:jan_novak>` or `Jan Novák <jan.novak@seznam.cz>`.
 * The bracketed handle is resolved to a link the status page can render.
 */
export interface OperatorContact {
	/** Display name of the operator. Falls back to the contact label if absent. */
	name: string;
	/** Human-readable contact (handle, email, host), or undefined if none given. */
	label?: string;
	/** Resolved URL for the contact, or undefined if it can't be linked. */
	href?: string;
}

/** Parse an operator string into a name plus an optional resolved contact. */
export function parseOperator(raw?: string | null): OperatorContact | null {
	if (!raw) return null;
	const trimmed = raw.trim();
	if (!trimmed) return null;
	const match = trimmed.match(/^(.*?)\s*<([^>]+)>\s*$/);
	const name = (match ? match[1] : trimmed).trim();
	const contact = match ? match[2].trim() : '';
	if (!contact) return { name };
	const resolved = resolveContact(contact);
	return { name: name || resolved.label || contact, ...resolved };
}

/**
 * Map a contact handle to a label and link. Supports `telegram:`, `x:`,
 * `mastodon:`, and `web:` schemes, bare http(s) URLs, and plain email addresses.
 */
function resolveContact(contact: string): { label: string; href?: string } {
	const colon = contact.indexOf(':');
	const scheme = colon > 0 ? contact.slice(0, colon).toLowerCase() : '';
	const value = colon > 0 ? contact.slice(colon + 1).trim() : '';
	switch (scheme) {
		case 'telegram': {
			const handle = value.replace(/^@/, '');
			return { label: `@${handle}`, href: `https://t.me/${handle}` };
		}
		case 'x':
		case 'twitter': {
			const handle = value.replace(/^@/, '');
			return { label: `@${handle}`, href: `https://x.com/${handle}` };
		}
		case 'mastodon': {
			const v = value.replace(/^@/, '');
			const at = v.lastIndexOf('@');
			if (at > 0) {
				const user = v.slice(0, at);
				const instance = v.slice(at + 1);
				return { label: `@${user}@${instance}`, href: `https://${instance}/@${user}` };
			}
			return { label: value };
		}
		case 'web':
		case 'http':
		case 'https': {
			const url = scheme === 'web' ? value : contact;
			const href = /^https?:\/\//i.test(url) ? url : `https://${url}`;
			const label = href.replace(/^https?:\/\//i, '').replace(/\/$/, '');
			return { label, href };
		}
	}
	if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(contact)) {
		return { label: contact, href: `mailto:${contact}` };
	}
	return { label: contact };
}
