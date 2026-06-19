package main

import (
	"context"
	"errors"
	"sync"

	meshcore "github.com/meshcore-cz/meshcore-go"
)

type CompanionRadio struct {
	uri string

	mu     sync.RWMutex
	client *meshcore.Client
	ready  bool
}

func NewCompanionRadio(uri string) *CompanionRadio {
	return &CompanionRadio{uri: uri}
}

func (r *CompanionRadio) Run(ctx context.Context, observe func(ObservedPacket)) error {
	client, err := meshcore.Dial(ctx, r.uri)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.client = client
	r.ready = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.ready = false
		r.client = nil
		r.mu.Unlock()
		_ = client.Close()
	}()

	events, unsubscribe := client.SubscribeEvents(256)
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return errors.New("companion event stream closed")
			}
			switch event := event.(type) {
			case meshcore.RFPacketReceived:
				rssi := event.RSSI
				snr := event.SNR
				observe(ObservedPacket{
					Bytes:     event.Bytes,
					Timestamp: event.Timestamp,
					RSSI:      &rssi,
					SNR:       &snr,
				})
			case meshcore.Disconnected:
				return event.Err
			}
		}
	}
}

func (r *CompanionRadio) SendMeshPacket(ctx context.Context, packet []byte) error {
	r.mu.RLock()
	client := r.client
	ready := r.ready
	r.mu.RUnlock()
	if !ready || client == nil {
		return errors.New("companion is not connected")
	}
	return client.SendMeshPacket(ctx, 0, packet)
}

func (r *CompanionRadio) Ready() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ready && r.client != nil
}

func (r *CompanionRadio) Close() error {
	r.mu.Lock()
	client := r.client
	r.client = nil
	r.ready = false
	r.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}
