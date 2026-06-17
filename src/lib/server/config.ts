import { existsSync, readFileSync } from 'node:fs';
import { parse } from 'yaml';
import type { EndpointConfig } from '../types';

const defaultCoreScopeUrls = ['wss://analyzer.meshcore.cz', 'wss://mc.pp0.co'];

export interface AppConfig {
	databasePath: string;
	coreScopeUrls: string[];
	nodeApiUrls: string[];
	observerApiUrls: string[];
	agentSecret: string;
	privateKey?: string;
	publicKey?: string;
	serviceName: string;
	verbose: boolean;
	autoReply: boolean;
	endpoints: EndpointConfig[];
	testTtlMinutes: number;
}

interface YamlConfig {
	service?: {
		name?: string;
		databasePath?: string;
		verbose?: boolean;
		autoReply?: boolean;
		testTtlMinutes?: number;
		privateKey?: string;
		publicKey?: string;
		agentSecret?: string;
	};
	coreScope?: {
		urls?: string[];
		nodeApiUrls?: string[];
		observerApiUrls?: string[];
	};
	endpoints?: EndpointConfig[];
}

function readYamlConfig(path: string): YamlConfig {
	if (!existsSync(path)) {
		throw new Error(`Missing configuration file: ${path}`);
	}

	try {
		return (parse(readFileSync(path, 'utf8')) as YamlConfig | null) || {};
	} catch (error) {
		throw new Error(
			`${path} is not valid YAML: ${error instanceof Error ? error.message : String(error)}`,
			{ cause: error }
		);
	}
}

export function getConfig(path = 'config.yaml'): AppConfig {
	const yaml = readYamlConfig(path);
	const service = yaml.service || {};
	const coreScope = yaml.coreScope || {};
	const coreScopeUrls = coreScope.urls || defaultCoreScopeUrls;
	const nodeApiUrls =
		coreScope.nodeApiUrls ||
		coreScopeUrls.map(
			(url) =>
				url.replace(/^wss:\/\//, 'https://').replace(/^ws:\/\//, 'http://') +
				'/api/nodes?limit=2000&offset=0'
		);
	const observerApiUrls =
		coreScope.observerApiUrls ||
		coreScopeUrls.map(
			(url) =>
				url.replace(/^wss:\/\//, 'https://').replace(/^ws:\/\//, 'http://') +
				'/api/observers'
		);
	const endpoints = normalizeEndpoints(yaml.endpoints || [], service.privateKey);

	const config: AppConfig = {
		databasePath: service.databasePath || 'data/hopback.sqlite',
		coreScopeUrls,
		nodeApiUrls,
		observerApiUrls,
		agentSecret: service.agentSecret || '',
		privateKey: service.privateKey,
		publicKey: service.publicKey,
		serviceName: service.name || 'Hopback',
		verbose: Boolean(service.verbose),
		autoReply: service.autoReply !== false,
		endpoints,
		testTtlMinutes: Number(service.testTtlMinutes || 20)
	};

	validateConfig(config);
	return config;
}

function normalizeEndpoints(
	endpoints: EndpointConfig[],
	fallbackPrivateKey?: string
): EndpointConfig[] {
	return endpoints
		.filter((endpoint) => endpoint.id && endpoint.name && endpoint.publicKey)
		.map((endpoint) => ({
			...endpoint,
			publicKey: endpoint.publicKey.toLowerCase(),
			region: endpoint.region || endpoint.location?.label || endpoint.host || 'MeshCore',
			privateKey: endpoint.privateKey || fallbackPrivateKey
		}));
}

function validateConfig(config: AppConfig) {
	const errors: string[] = [];

	if (!config.serviceName.trim()) errors.push('service.name is required');
	if (!config.databasePath.trim()) errors.push('service.databasePath is required');
	if (!config.agentSecret.trim()) errors.push('service.agentSecret is required');
	if (!config.coreScopeUrls.length)
		errors.push('coreScope.urls must contain at least one websocket URL');
	if (!config.nodeApiUrls.length)
		errors.push('coreScope.nodeApiUrls must contain at least one node API URL');
	if (!config.observerApiUrls.length)
		errors.push('coreScope.observerApiUrls must contain at least one observer API URL');
	if (!Number.isFinite(config.testTtlMinutes) || config.testTtlMinutes <= 0) {
		errors.push('service.testTtlMinutes must be a positive number');
	}

	if (!config.endpoints.length) {
		errors.push('endpoints must contain at least one endpoint');
	}

	for (const endpoint of config.endpoints) {
		const prefix = `endpoints.${endpoint.id || '<missing-id>'}`;
		if (!endpoint.id) errors.push('endpoint.id is required');
		if (!endpoint.name) errors.push(`${prefix}.name is required`);
		if (!endpoint.host) errors.push(`${prefix}.host is required`);
		if (!endpoint.publicKey || !isHex(endpoint.publicKey, 32)) {
			errors.push(`${prefix}.publicKey must be 64 hex characters`);
		}
		if (
			!Number.isInteger(endpoint.type) ||
			!endpoint.type ||
			endpoint.type < 1 ||
			endpoint.type > 4
		) {
			errors.push(`${prefix}.type must be one of 1, 2, 3, 4`);
		}
		if (
			!endpoint.privateKey ||
			(!isHex(endpoint.privateKey, 32) && !isHex(endpoint.privateKey, 64))
		) {
			errors.push(`${prefix}.privateKey must be 64 or 128 hex characters`);
		}
		if (!endpoint.location?.label) errors.push(`${prefix}.location.label is required`);
		if (!Number.isFinite(endpoint.location?.lat)) errors.push(`${prefix}.location.lat is required`);
		if (!Number.isFinite(endpoint.location?.lon)) errors.push(`${prefix}.location.lon is required`);
	}

	if (errors.length) {
		throw new Error(`Invalid Hopback configuration:\n- ${errors.join('\n- ')}`);
	}
}

function isHex(value: string, bytes: 32 | 64) {
	return new RegExp(`^[0-9a-fA-F]{${bytes * 2}}$`).test(value);
}
