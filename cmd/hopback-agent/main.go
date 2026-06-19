package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meshcore-cz/hopback/internal/buildinfo"
)

const (
	rfWatchRequestID = 1
	sendTimeout      = 10 * time.Second
)

type AgentConfig struct {
	BackendURL string
	Secret     string
	EndpointID string
	AgentID    string
	IPCPath    string
	IPCHost    string
	IPCPort    int
	IPCDevice  string
}

type Agent struct {
	cfg              AgentConfig
	backend          *websocket.Conn
	ipc              net.Conn
	ipcConnectedOnce bool
	rfSubscribedOnce bool
	rfWatching       bool
	ipcStartupFailed bool
	requestID        int
	mu               sync.Mutex
}

type BackendMessage struct {
	Type       string `json:"type"`
	TestID     string `json:"testId,omitempty"`
	PacketRole string `json:"packetRole,omitempty"`
	RawHex     string `json:"rawHex,omitempty"`
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

func main() {
	loadDotEnv(".env")
	cfg, err := loadAgentConfig()
	if err != nil {
		log.Fatal(err)
	}
	agent := &Agent{cfg: cfg, requestID: rfWatchRequestID}
	agent.connectIPC()
	select {}
}

func loadAgentConfig() (AgentConfig, error) {
	port := 0
	if value := envValue("MESHCORE_IPC_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return AgentConfig{}, fmt.Errorf("MESHCORE_IPC_PORT must be a number: %w", err)
		}
		port = parsed
	}
	cfg := AgentConfig{
		BackendURL: mustEnv("HOPBACK_BACKEND_WS"),
		Secret:     mustEnv("HOPBACK_AGENT_SECRET"),
		EndpointID: mustEnv("HOPBACK_ENDPOINT_ID"),
		AgentID:    envValue("HOPBACK_AGENT_ID"),
		IPCPath:    expandHome(envValue("MESHCORE_IPC_PATH")),
		IPCHost:    envValue("MESHCORE_IPC_HOST"),
		IPCPort:    port,
		IPCDevice:  envValue("MESHCORE_DEVICE"),
	}
	if cfg.AgentID == "" {
		cfg.AgentID = "agent-" + cfg.EndpointID
	}
	if cfg.IPCPath == "" && (cfg.IPCHost == "" || cfg.IPCPort == 0) {
		return AgentConfig{}, errors.New("MESHCORE_IPC_PATH or both MESHCORE_IPC_HOST and MESHCORE_IPC_PORT are required")
	}
	return cfg, nil
}

func (a *Agent) connectBackend() {
	u, err := url.Parse(a.cfg.BackendURL)
	if err != nil {
		log.Printf("[agent] bad backend URL: %v", err)
		time.AfterFunc(3*time.Second, a.connectBackend)
		return
	}
	u.Path = "/agent"
	q := u.Query()
	q.Set("secret", a.cfg.Secret)
	q.Set("id", a.cfg.AgentID)
	q.Set("endpointId", a.cfg.EndpointID)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("[agent] backend error: %v", err)
		time.AfterFunc(3*time.Second, a.connectBackend)
		return
	}

	a.mu.Lock()
	if a.backend != nil {
		_ = a.backend.Close()
	}
	a.backend = conn
	a.mu.Unlock()

	log.Printf("[agent] connected to %s", u.Scheme+"://"+u.Host)
	a.sendBackendStatus()
	for {
		var msg BackendMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == "sendRaw" && msg.RawHex != "" {
			go a.sendRaw(msg)
		}
	}

	a.mu.Lock()
	if a.backend == conn {
		a.backend = nil
	}
	a.mu.Unlock()
	_ = conn.Close()
	log.Printf("[agent] backend disconnected")
	time.AfterFunc(3*time.Second, a.connectBackend)
}

func (a *Agent) connectIPC() {
	a.mu.Lock()
	a.rfWatching = false
	a.mu.Unlock()

	conn, err := a.createIPCSocket()
	if err != nil {
		log.Printf("[agent] IPC error: %v", err)
		if !a.ipcConnectedOnce {
			a.failStartup(fmt.Sprintf("cannot connect to meshcore-go IPC: %v", err))
		}
		time.AfterFunc(3*time.Second, a.connectIPC)
		return
	}

	a.mu.Lock()
	a.ipc = conn
	a.ipcConnectedOnce = true
	a.mu.Unlock()
	log.Printf("[agent] meshcore-go IPC connected")

	if err := writeIPCRequest(conn, rfWatchRequestID, a.cfg.IPCDevice, "watch_rf", nil); err != nil {
		log.Printf("[agent] IPC write error: %v", err)
		_ = conn.Close()
		time.AfterFunc(3*time.Second, a.connectIPC)
		return
	}

	a.mu.Lock()
	needBackend := a.backend == nil
	a.mu.Unlock()
	if needBackend {
		go a.connectBackend()
	}
	a.sendBackendStatus()
	a.readIPCLoop(conn)
}

func (a *Agent) readIPCLoop(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			a.handleIPCLine(line, conn)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[agent] IPC read error: %v", err)
	}

	a.mu.Lock()
	if a.ipc == conn {
		a.ipc = nil
	}
	a.rfWatching = false
	a.mu.Unlock()
	_ = conn.Close()
	log.Printf("[agent] meshcore-go IPC disconnected")
	if !a.ipcConnectedOnce {
		a.failStartup("meshcore-go IPC closed before connecting")
	}
	a.sendBackendStatus()
	time.AfterFunc(3*time.Second, a.connectIPC)
}

func (a *Agent) handleIPCLine(line string, conn net.Conn) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		log.Printf("[agent] bad IPC JSON: %v", err)
		return
	}
	if _, ok := raw["id"]; ok {
		var response IPCResponse
		if err := json.Unmarshal([]byte(line), &response); err == nil {
			if !response.OK {
				log.Printf("[agent] RF subscription rejected: %s", firstNonEmpty(response.Error, "unknown error"))
				if !a.rfSubscribedOnce {
					a.failStartup(firstNonEmpty(response.Error, "RF subscription rejected"))
				}
				_ = conn.Close()
				return
			}
			a.mu.Lock()
			a.rfSubscribedOnce = true
			a.rfWatching = true
			a.mu.Unlock()
			log.Printf("[agent] observing MeshCore RF packets")
			a.sendBackendStatus()
			return
		}
	}

	var event RFEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil || event.Bytes == "" {
		log.Printf("[agent] unexpected IPC message: %s", line)
		return
	}
	bytes, err := base64.StdEncoding.DecodeString(event.Bytes)
	if err != nil {
		log.Printf("[agent] bad RF bytes: %v", err)
		return
	}
	_ = a.sendBackend(map[string]any{
		"type":      "observedPacket",
		"rawHex":    hex.EncodeToString(bytes),
		"timestamp": event.Timestamp,
		"rssi":      event.RSSI,
		"snr":       event.SNR,
	})
}

func (a *Agent) sendRaw(msg BackendMessage) {
	response, err := a.sendMeshPacket(msg.RawHex)
	if err != nil {
		response = IPCResponse{OK: false, Error: err.Error()}
	}
	_ = a.sendBackend(map[string]any{
		"type":       "sendRawResult",
		"testId":     msg.TestID,
		"packetRole": msg.PacketRole,
		"rawHex":     msg.RawHex,
		"ok":         response.OK,
		"error":      response.Error,
	})
}

func (a *Agent) sendMeshPacket(rawHex string) (IPCResponse, error) {
	if !a.isIPCReady() {
		return IPCResponse{OK: false, Error: "IPC is not connected"}, nil
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return IPCResponse{}, err
	}
	return a.sendIPCRequest("send_mesh_packet", map[string]any{
		"priority": 0,
		"packet":   base64.StdEncoding.EncodeToString(raw),
	})
}

func (a *Agent) sendIPCRequest(method string, params map[string]any) (IPCResponse, error) {
	conn, err := a.createIPCSocket()
	if err != nil {
		return IPCResponse{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))

	id := a.nextRequestID()
	if err := writeIPCRequest(conn, id, a.cfg.IPCDevice, method, params); err != nil {
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

func (a *Agent) createIPCSocket() (net.Conn, error) {
	if a.cfg.IPCPath != "" {
		return net.DialTimeout("unix", a.cfg.IPCPath, sendTimeout)
	}
	return net.DialTimeout("tcp", net.JoinHostPort(a.cfg.IPCHost, strconv.Itoa(a.cfg.IPCPort)), sendTimeout)
}

func (a *Agent) isIPCReady() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ipc != nil && a.rfWatching
}

func (a *Agent) sendBackendStatus() {
	_ = a.sendBackend(map[string]any{
		"type":       "hello",
		"id":         a.cfg.AgentID,
		"version":    buildinfo.Version,
		"endpointId": a.cfg.EndpointID,
		"ipcReady":   a.isIPCReady(),
	})
}

func (a *Agent) sendBackend(payload any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backend == nil {
		return errors.New("backend is not connected")
	}
	return a.backend.WriteJSON(payload)
}

func (a *Agent) nextRequestID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requestID++
	return a.requestID
}

func (a *Agent) failStartup(message string) {
	a.mu.Lock()
	if a.ipcStartupFailed {
		a.mu.Unlock()
		os.Exit(1)
	}
	a.ipcStartupFailed = true
	a.mu.Unlock()
	log.Fatalf("[agent] startup failed: %s", message)
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

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		_ = os.Setenv(key, unquoteEnvValue(strings.TrimSpace(value)))
	}
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func envValue(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func mustEnv(name string) string {
	value := envValue(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func expandHome(value string) string {
	if value == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, value[2:])
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
