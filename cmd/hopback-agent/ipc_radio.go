package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const rfWatchRequestID = 1

type IPCRadio struct {
	uri    string
	device string

	mu             sync.Mutex
	conn           net.Conn
	ready          bool
	requestID      int
	subscribedOnce bool
}

type IPCResponse struct {
	ID    int    `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type RFEvent struct {
	Timestamp string   `json:"timestamp"`
	SNR       *float64 `json:"snr"`
	RSSI      *float64 `json:"rssi"`
	Bytes     string   `json:"bytes"`
}

func NewIPCRadio(uri, device string) *IPCRadio {
	return &IPCRadio{uri: uri, device: device, requestID: rfWatchRequestID}
}

func (r *IPCRadio) Run(ctx context.Context, observe func(ObservedPacket)) error {
	r.setReady(false)
	conn, err := r.openSocket()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conn == conn {
			r.conn = nil
		}
		r.ready = false
		r.mu.Unlock()
		_ = conn.Close()
	}()

	if err := writeIPCRequest(conn, rfWatchRequestID, r.device, "watch_rf", nil); err != nil {
		return err
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := r.handleLine(line, conn, observe); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("IPC socket closed")
}

func (r *IPCRadio) SendMeshPacket(ctx context.Context, packet []byte) error {
	response, err := r.sendIPCRequest("send_mesh_packet", map[string]any{
		"priority": 0,
		"packet":   base64.StdEncoding.EncodeToString(packet),
	})
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(firstNonEmpty(response.Error, "send_mesh_packet rejected"))
	}
	return nil
}

func (r *IPCRadio) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready && r.conn != nil
}

func (r *IPCRadio) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = false
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	return err
}

func (r *IPCRadio) handleLine(line string, conn net.Conn, observe func(ObservedPacket)) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		log.Printf("[agent] bad IPC JSON: %v", err)
		return nil
	}
	if _, ok := raw["id"]; ok {
		var response IPCResponse
		if err := json.Unmarshal([]byte(line), &response); err == nil {
			if !response.OK {
				err := errors.New(firstNonEmpty(response.Error, "RF subscription rejected"))
				if !r.subscribedOnce {
					return err
				}
				_ = conn.Close()
				return err
			}
			r.mu.Lock()
			r.subscribedOnce = true
			r.ready = true
			r.mu.Unlock()
			log.Printf("[agent] observing MeshCore RF packets via IPC")
			return nil
		}
	}

	var event RFEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil || event.Bytes == "" {
		log.Printf("[agent] unexpected IPC message: %s", line)
		return nil
	}
	bytes, err := base64.StdEncoding.DecodeString(event.Bytes)
	if err != nil {
		log.Printf("[agent] bad RF bytes: %v", err)
		return nil
	}
	observe(ObservedPacket{
		Bytes:     bytes,
		Timestamp: parseIPCTimestamp(event.Timestamp),
		RSSI:      floatToIntPtr(event.RSSI),
		SNR:       event.SNR,
	})
	return nil
}

func (r *IPCRadio) sendIPCRequest(method string, params map[string]any) (IPCResponse, error) {
	conn, err := r.openSocket()
	if err != nil {
		return IPCResponse{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))

	id := r.nextRequestID()
	if err := writeIPCRequest(conn, id, r.device, method, params); err != nil {
		return IPCResponse{}, err
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var response IPCResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			return IPCResponse{}, err
		}
		return response, nil
	}
	if err := scanner.Err(); err != nil {
		return IPCResponse{}, err
	}
	return IPCResponse{}, fmt.Errorf("%s IPC socket closed before response", method)
}

func (r *IPCRadio) openSocket() (net.Conn, error) {
	u, err := url.Parse(r.uri)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ipc+unix":
		path := u.Path
		if path == "" {
			path = u.Opaque
		}
		if path == "" {
			return nil, fmt.Errorf("IPC URI %q has no socket path", r.uri)
		}
		return net.DialTimeout("unix", path, sendTimeout)
	case "ipc+tcp":
		if u.Host == "" {
			return nil, fmt.Errorf("IPC URI %q has no host:port", r.uri)
		}
		return net.DialTimeout("tcp", u.Host, sendTimeout)
	default:
		return nil, fmt.Errorf("unsupported IPC URI scheme %q", u.Scheme)
	}
}

func (r *IPCRadio) setReady(ready bool) {
	r.mu.Lock()
	r.ready = ready
	r.mu.Unlock()
}

func (r *IPCRadio) nextRequestID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestID++
	return r.requestID
}

func writeIPCRequest(conn net.Conn, id int, device, method string, params map[string]any) error {
	request := map[string]any{"id": id, "method": method}
	if device != "" {
		request["device"] = device
	}
	if params != nil {
		request["params"] = params
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func parseIPCTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func floatToIntPtr(value *float64) *int {
	if value == nil {
		return nil
	}
	rounded := int(*value)
	return &rounded
}
