package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const sendTimeout = 10 * time.Second

type Radio interface {
	Run(ctx context.Context, observe func(ObservedPacket)) error
	SendMeshPacket(ctx context.Context, packet []byte) error
	Ready() bool
	Close() error
}

type ObservedPacket struct {
	Bytes     []byte
	Timestamp time.Time
	RSSI      *int
	SNR       *float64
}

func newRadio(cfg AgentConfig) (Radio, error) {
	u, err := url.Parse(cfg.MeshCoreURI)
	if err != nil {
		return nil, fmt.Errorf("invalid MESHCORE_URI %q: %w", cfg.MeshCoreURI, err)
	}
	switch u.Scheme {
	case "ipc+unix", "ipc+tcp":
		return NewIPCRadio(cfg.MeshCoreURI, cfg.MeshCoreDevice), nil
	case "tcp":
		return NewCompanionRadio(cfg.MeshCoreURI), nil
	default:
		return nil, fmt.Errorf("unsupported MESHCORE_URI scheme %q", u.Scheme)
	}
}

func meshCoreURIFromEnv() (string, error) {
	if value := envValue("MESHCORE_URI"); value != "" {
		return expandHomeURI(value), nil
	}
	if path := expandHome(envValue("MESHCORE_IPC_PATH")); path != "" {
		return "ipc+unix://" + path, nil
	}
	host := envValue("MESHCORE_IPC_HOST")
	portText := envValue("MESHCORE_IPC_PORT")
	if host != "" || portText != "" {
		if host == "" || portText == "" {
			return "", errors.New("both MESHCORE_IPC_HOST and MESHCORE_IPC_PORT are required for TCP IPC")
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return "", fmt.Errorf("MESHCORE_IPC_PORT must be a number: %w", err)
		}
		return "ipc+tcp://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	return "", errors.New("MESHCORE_URI, MESHCORE_IPC_PATH, or both MESHCORE_IPC_HOST and MESHCORE_IPC_PORT are required")
}

func expandHomeURI(raw string) string {
	if strings.HasPrefix(raw, "ipc+unix://~/") {
		return "ipc+unix://" + expandHome(strings.TrimPrefix(raw, "ipc+unix://"))
	}
	return raw
}

func (a *Agent) connectRadio() {
	readyWas := false
	done := make(chan error, 1)
	go func() {
		done <- a.radio.Run(context.Background(), a.observePacket)
	}()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if readyWas {
				logRadioDisconnected(err)
			} else {
				logRadioError(err)
				if !a.radioConnectedOnce {
					a.failStartup(fmt.Sprintf("cannot connect to MeshCore radio: %v", err))
				}
			}
			a.sendBackendStatus()
			time.AfterFunc(reconnectDelay, a.connectRadio)
			return
		case <-ticker.C:
			ready := a.radio.Ready()
			if ready == readyWas {
				continue
			}
			readyWas = ready
			if ready {
				a.radioConnectedOnce = true
				log.Printf("[agent] MeshCore radio connected via %s", a.cfg.MeshCoreURI)
			}
			a.sendBackendStatus()
		}
	}
}

func (a *Agent) observePacket(packet ObservedPacket) {
	var timestamp any
	if !packet.Timestamp.IsZero() {
		timestamp = packet.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	_ = a.sendBackend(map[string]any{
		"type":      "observedPacket",
		"rawHex":    hex.EncodeToString(packet.Bytes),
		"timestamp": timestamp,
		"rssi":      packet.RSSI,
		"snr":       packet.SNR,
	})
}

func logRadioError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[agent] radio error: %v", err)
	}
}

func logRadioDisconnected(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[agent] MeshCore radio disconnected: %v", err)
		return
	}
	log.Printf("[agent] MeshCore radio disconnected")
}
