import type { DiagnosticTest, PacketKind, PacketObservation, TestStatus } from './types';

const ACK_TYPES = new Set(['ACK', 'PATH', 'PATH_IDENTITY', 'RESPONSE', 'MULTIPART']);

export interface TestMilestones {
	outboundSeenAt: string | null;
	outboundEndpointSeenAt: string | null;
	outboundAckSeenAt: string | null;
	replyBroadcastAt: string | null;
	replySeenAt: string | null;
	replyAckSeenAt: string | null;
	replyEndpointAckAt: string | null;
}

export function deriveMilestones(test: DiagnosticTest): TestMilestones {
	const outbound = firstObservation(
		test.observations,
		(item) => item.direction === 'outbound' && !isAckObservation(item)
	);
	const outboundEndpoint = firstObservation(
		test.observations,
		(item) =>
			item.direction === 'outbound' && !isAckObservation(item) && isEndpointObservation(item, test)
	);
	const outboundAck = firstObservation(
		test.observations,
		(item) => item.direction === 'outbound' && isAckObservation(item)
	);
	const replySeen = firstObservation(
		test.observations,
		(item) => item.direction === 'return' && !isAckObservation(item)
	);
	const replyAck = firstObservation(
		test.observations,
		(item) => item.direction === 'return' && isAckObservation(item)
	);
	const replyEndpointAck = firstObservation(
		test.observations,
		(item) =>
			item.direction === 'return' && isAckObservation(item) && isEndpointObservation(item, test)
	);

	return {
		outboundSeenAt: test.outboundSeenAt || outbound?.createdAt || null,
		outboundEndpointSeenAt: test.outboundEndpointSeenAt || outboundEndpoint?.createdAt || null,
		outboundAckSeenAt: test.outboundAckSeenAt || outboundAck?.createdAt || null,
		replyBroadcastAt: test.replyBroadcastAt || null,
		replySeenAt: test.returnSeenAt || replySeen?.createdAt || null,
		replyAckSeenAt: test.replyAckSeenAt || replyAck?.createdAt || null,
		replyEndpointAckAt: test.replyEndpointAckAt || replyEndpointAck?.createdAt || null
	};
}

export function deriveTestStatus(test: DiagnosticTest, now = Date.now()): TestStatus {
	const milestones = deriveMilestones(test);
	if (milestones.replyEndpointAckAt) return 'completed';
	if (new Date(test.expiresAt).getTime() <= now) return 'expired';
	if (test.status === 'failed') return 'failed';
	if (milestones.replyBroadcastAt || milestones.replySeenAt || milestones.replyAckSeenAt)
		return 'replying';
	if (milestones.outboundSeenAt || milestones.outboundEndpointSeenAt) return 'detected';
	return 'waiting';
}

export function withDerivedStatus(test: DiagnosticTest): DiagnosticTest {
	return {
		...test,
		status: deriveTestStatus(test)
	};
}

export function isAckObservation(observation: PacketObservation) {
	return ACK_TYPES.has(observation.decodedType || '');
}

/** Single source of truth for the packet "kind" shown in the table and map legend. */
export function packetKind(
	observation: Pick<PacketObservation, 'direction' | 'decodedType'>
): PacketKind {
	const isAck = ACK_TYPES.has(observation.decodedType || '');
	if (isAck) {
		if (observation.direction === 'outbound') {
			return observation.decodedType === 'PATH' || observation.decodedType === 'PATH_IDENTITY'
				? 'ack+path'
				: 'ack';
		}
		return 'reply ack';
	}
	return observation.direction === 'outbound' ? 'user msg' : 'reply';
}

export function isReplyAck(observation: PacketObservation) {
	return observation.direction === 'return' && isAckObservation(observation);
}

export function isEndpointObservation(observation: PacketObservation, test: DiagnosticTest) {
	const observer = (observation.observerId || observation.observerKey || '').toLowerCase();
	const endpointKey = test.endpointPublicKey.toLowerCase();
	return (
		observation.source.startsWith('agent:') ||
		observer === endpointKey ||
		Boolean(observation.observerName?.toLowerCase().includes(test.endpointName.toLowerCase()))
	);
}

function firstObservation(
	observations: PacketObservation[],
	predicate: (observation: PacketObservation) => boolean
) {
	return observations
		.filter(predicate)
		.reduce<PacketObservation | null>((earliest, observation) => {
			if (!earliest) return observation;
			return new Date(observation.createdAt).getTime() < new Date(earliest.createdAt).getTime()
				? observation
				: earliest;
		}, null);
}
