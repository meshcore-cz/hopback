import { describe, it, expect } from 'vitest';
import { parseOperator } from './operator';

describe('parseOperator', () => {
	it('returns null for empty input', () => {
		expect(parseOperator()).toBeNull();
		expect(parseOperator('  ')).toBeNull();
	});

	it('parses a bare name without a contact', () => {
		expect(parseOperator('Jan Novák')).toEqual({ name: 'Jan Novák' });
	});

	it('resolves telegram handles', () => {
		expect(parseOperator('Jan Novák <telegram:jan_novak>')).toEqual({
			name: 'Jan Novák',
			label: '@jan_novak',
			href: 'https://t.me/jan_novak'
		});
	});

	it('resolves x handles, stripping a leading @', () => {
		expect(parseOperator('Jan <x:@jan_novak>')).toEqual({
			name: 'Jan',
			label: '@jan_novak',
			href: 'https://x.com/jan_novak'
		});
	});

	it('resolves mastodon handles to the instance', () => {
		expect(parseOperator('Jan <mastodon:@jan@mastodon.social>')).toEqual({
			name: 'Jan',
			label: '@jan@mastodon.social',
			href: 'https://mastodon.social/@jan'
		});
	});

	it('resolves web handles, adding a scheme when missing', () => {
		expect(parseOperator('Jan <web:example.com/jan>')).toEqual({
			name: 'Jan',
			label: 'example.com/jan',
			href: 'https://example.com/jan'
		});
	});

	it('resolves a plain email to a mailto link', () => {
		expect(parseOperator('Jan Novák <jan.novak@seznam.cz>')).toEqual({
			name: 'Jan Novák',
			label: 'jan.novak@seznam.cz',
			href: 'mailto:jan.novak@seznam.cz'
		});
	});

	it('keeps an unknown contact as a plain label', () => {
		expect(parseOperator('Jan <signal:123>')).toEqual({ name: 'Jan', label: 'signal:123' });
	});
});
