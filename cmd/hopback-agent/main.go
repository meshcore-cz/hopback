package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meshcore-cz/hopback/internal/buildinfo"
)

// startedAt records the agent process start so the backend can show agent uptime.
var startedAt = time.Now().UTC().Format(time.RFC3339)

const (
	reconnectDelay = 3 * time.Second
)

type AgentConfig struct {
	BackendURL     string
	Secret         string
	MeshCoreURI    string
	MeshCoreDevice string
}

type Agent struct {
	cfg                AgentConfig
	radio              Radio
	backend            *websocket.Conn
	radioConnectedOnce bool
	radioStartupFailed bool
	mu                 sync.Mutex
}

type BackendMessage struct {
	Type       string `json:"type"`
	TestID     string `json:"testId,omitempty"`
	PacketRole string `json:"packetRole,omitempty"`
	RawHex     string `json:"rawHex,omitempty"`
}

func main() {
	loadDotEnv(".env")
	cfg, err := loadAgentConfig()
	if err != nil {
		log.Fatal(err)
	}
	radio, err := newRadio(cfg)
	if err != nil {
		log.Fatal(err)
	}
	agent := &Agent{cfg: cfg, radio: radio}
	go agent.connectBackend()
	agent.connectRadio()
	select {}
}

func loadAgentConfig() (AgentConfig, error) {
	meshURI, err := meshCoreURIFromEnv()
	if err != nil {
		return AgentConfig{}, err
	}
	cfg := AgentConfig{
		BackendURL:     mustEnv("HOPBACK_BACKEND_WS"),
		Secret:         mustEnv("HOPBACK_AGENT_SECRET"),
		MeshCoreURI:    meshURI,
		MeshCoreDevice: envValue("MESHCORE_DEVICE"),
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
	// The backend derives this agent's endpoint and id from the secret alone.
	q.Set("secret", a.cfg.Secret)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("[agent] backend error: %v", err)
		time.AfterFunc(reconnectDelay, a.connectBackend)
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
	time.AfterFunc(reconnectDelay, a.connectBackend)
}

func (a *Agent) sendRaw(msg BackendMessage) {
	err := a.sendMeshPacket(msg.RawHex)
	ok := err == nil
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	_ = a.sendBackend(map[string]any{
		"type":       "sendRawResult",
		"testId":     msg.TestID,
		"packetRole": msg.PacketRole,
		"rawHex":     msg.RawHex,
		"ok":         ok,
		"error":      errText,
	})
}

func (a *Agent) sendMeshPacket(rawHex string) error {
	if !a.radio.Ready() {
		return errors.New("radio is not connected")
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	return a.radio.SendMeshPacket(ctx, raw)
}

func (a *Agent) sendBackendStatus() {
	_ = a.sendBackend(map[string]any{
		"type":      "hello",
		"version":   buildinfo.Version,
		"ipcReady":  a.radio.Ready(),
		"platform":  runtime.GOOS + "/" + runtime.GOARCH,
		"startedAt": startedAt,
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

func (a *Agent) failStartup(message string) {
	a.mu.Lock()
	if a.radioStartupFailed {
		a.mu.Unlock()
		os.Exit(1)
	}
	a.radioStartupFailed = true
	a.mu.Unlock()
	log.Fatalf("[agent] startup failed: %s", message)
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
