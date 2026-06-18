package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"math/rand/v2"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-cz/meshpkt"
	qrcode "github.com/skip2/go-qrcode"
	"gopkg.in/yaml.v3"
)

const (
	packetQueueSize = 4096
	maxPacketSeen   = 20000
)

//go:embed all:frontend
var frontendFiles embed.FS

type Config struct {
	Service struct {
		Name           string `yaml:"name"`
		DatabasePath   string `yaml:"databasePath"`
		Verbose        bool   `yaml:"verbose"`
		AutoReply      *bool  `yaml:"autoReply"`
		TestTTLMinutes int    `yaml:"testTtlMinutes"`
		PrivateKey     string `yaml:"privateKey"`
		PublicKey      string `yaml:"publicKey"`
		AgentSecret    string `yaml:"agentSecret"`
	} `yaml:"service"`
	CoreScope struct {
		URLs            []string `yaml:"urls"`
		NodeAPIURLs     []string `yaml:"nodeApiUrls"`
		ObserverAPIURLs []string `yaml:"observerApiUrls"`
	} `yaml:"coreScope"`
	Endpoints []Endpoint `yaml:"endpoints"`
}

type Endpoint struct {
	ID         string    `json:"id" yaml:"id"`
	Host       string    `json:"host,omitempty" yaml:"host"`
	Name       string    `json:"name" yaml:"name"`
	Region     string    `json:"region" yaml:"region"`
	PublicKey  string    `json:"publicKey" yaml:"publicKey"`
	Type       int       `json:"type,omitempty" yaml:"type"`
	PrivateKey string    `json:"-" yaml:"privateKey"`
	AgentID    string    `json:"agentId,omitempty" yaml:"agentId"`
	Location   *Location `json:"location,omitempty" yaml:"location"`
}

type Location struct {
	Label string   `json:"label" yaml:"label"`
	Lat   *float64 `json:"lat,omitempty" yaml:"lat"`
	Lon   *float64 `json:"lon,omitempty" yaml:"lon"`
}

type RuntimeStatus struct {
	Analyzers       []AnalyzerState  `json:"analyzers"`
	Endpoints       []EndpointStatus `json:"endpoints"`
	Agents          []AgentStatus    `json:"agents"`
	Nodes           int              `json:"nodes"`
	Observers       int              `json:"observers"`
	ActiveObservers int              `json:"activeObservers"`
	ActiveTests     int              `json:"activeTests"`
	Verbose         bool             `json:"verbose"`
}

type AnalyzerState struct {
	URL           string `json:"url"`
	State         string `json:"state"`
	LastMessageAt string `json:"lastMessageAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

type EndpointStatus struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Region     string    `json:"region"`
	PublicKey  string    `json:"publicKey"`
	Host       string    `json:"host,omitempty"`
	Location   *Location `json:"location,omitempty"`
	Ready      bool      `json:"ready"`
	Connected  bool      `json:"connected"`
	AgentID    string    `json:"agentId,omitempty"`
	IPCReady   bool      `json:"ipcReady"`
	LastSeenAt string    `json:"lastSeenAt,omitempty"`
}

type AgentStatus struct {
	ID          string `json:"id"`
	EndpointID  string `json:"endpointId,omitempty"`
	IPCReady    bool   `json:"ipcReady"`
	ConnectedAt string `json:"connectedAt"`
	LastSeenAt  string `json:"lastSeenAt"`
}

type Test struct {
	ID                     string             `json:"id"`
	BrowserID              string             `json:"browserId"`
	UserPublicKey          string             `json:"userPublicKey"`
	EndpointID             string             `json:"endpointId"`
	EndpointName           string             `json:"endpointName"`
	EndpointRegion         string             `json:"endpointRegion"`
	EndpointPublicKey      string             `json:"endpointPublicKey"`
	EndpointLocation       *Location          `json:"endpointLocation,omitempty"`
	Code                   string             `json:"code"`
	Status                 string             `json:"status"`
	QRPayload              string             `json:"qrPayload"`
	QRDataURL              string             `json:"qrDataUrl,omitempty"`
	OutboundSeenAt         *string            `json:"outboundSeenAt,omitempty"`
	OutboundEndpointSeenAt *string            `json:"outboundEndpointSeenAt,omitempty"`
	OutboundAckSeenAt      *string            `json:"outboundAckSeenAt,omitempty"`
	ReplyBroadcastAt       *string            `json:"replyBroadcastAt,omitempty"`
	ReturnSeenAt           *string            `json:"returnSeenAt,omitempty"`
	ReplyAckSeenAt         *string            `json:"replyAckSeenAt,omitempty"`
	ReplyEndpointAckAt     *string            `json:"replyEndpointAckAt,omitempty"`
	OutboundHash           *string            `json:"outboundHash,omitempty"`
	OutboundAckHash        *string            `json:"outboundAckHash,omitempty"`
	OutboundAckCRCHex      *string            `json:"outboundAckCrcHex,omitempty"`
	ReturnHash             *string            `json:"returnHash,omitempty"`
	ReplyHash              *string            `json:"replyHash,omitempty"`
	ReplyAckHash           *string            `json:"replyAckHash,omitempty"`
	ReplyAckCRCHex         *string            `json:"replyAckCrcHex,omitempty"`
	OutboundHex            *string            `json:"outboundHex,omitempty"`
	OutboundAckHex         *string            `json:"outboundAckHex,omitempty"`
	ReturnHex              *string            `json:"returnHex,omitempty"`
	ReplyHex               *string            `json:"replyHex,omitempty"`
	ReplyAckHex            *string            `json:"replyAckHex,omitempty"`
	ReplyStatus            *string            `json:"replyStatus,omitempty"`
	CreatedAt              string             `json:"createdAt"`
	UpdatedAt              string             `json:"updatedAt"`
	ExpiresAt              string             `json:"expiresAt"`
	Observations           []Observation      `json:"observations"`
	Nodes                  map[string]NodeRef `json:"nodes"`
	ObservationCount       *int               `json:"observationCount,omitempty"`
	DeliveryPaths          map[string][]any   `json:"deliveryPaths,omitempty"`
	PropagationMap         map[string][]any   `json:"propagationMap,omitempty"`
	PathStatistics         *map[string]any    `json:"pathStatistics,omitempty"`
}

type Observation struct {
	ID           int64     `json:"id"`
	Direction    string    `json:"direction"`
	Source       string    `json:"source"`
	PacketHash   string    `json:"packetHash"`
	ObserverID   *string   `json:"observerId,omitempty"`
	ObserverName *string   `json:"observerName,omitempty"`
	ObserverKey  *string   `json:"observerKey,omitempty"`
	HopCount     int       `json:"hopCount"`
	Path         []string  `json:"path"`
	PathKeys     []*string `json:"pathKeys"`
	DecodedType  *string   `json:"decodedType,omitempty"`
	CreatedAt    string    `json:"createdAt"`
}

type NodeRecord struct {
	PublicKey string   `json:"publicKey"`
	Name      string   `json:"name"`
	ShortHash string   `json:"shortHash"`
	NodeType  *int     `json:"nodeType,omitempty"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	UpdatedAt string   `json:"updatedAt"`
	Source    string   `json:"source"`
}

type NodeRef struct {
	Name      string   `json:"name"`
	ShortHash string   `json:"shortHash"`
	PublicKey string   `json:"publicKey,omitempty"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

type Runtime struct {
	cfg       Config
	store     *Store
	upgrader  websocket.Upgrader
	packetCh  chan PacketEvent
	browsers  map[*BrowserClient]bool
	agents    map[string]*AgentClient
	analyzers map[string]AnalyzerState
	observers map[string]ObserverRecord
	active    map[string]*ActiveTest
	index     ActiveIndex
	seen      map[string]time.Time
	mu        sync.RWMutex
}

type ActiveTest struct {
	Test *Test
	Keys map[string]bool
}

type ActiveIndex struct {
	ByPair map[string][]*ActiveTest
	ByCRC  map[string]PendingMatch
}

type PendingMatch struct {
	TestID      string
	Direction   string
	DecodedType string
}

type PacketEvent struct {
	Source       string
	RawHex       string
	Hash         string
	ObserverID   *string
	ObserverName *string
	Timestamp    *string
	RSSI         *float64
	SNR          *float64
	Path         []string
	ResolvedPath []string
	PayloadType  *string
	Original     any
}

type BrowserClient struct {
	conn      *websocket.Conn
	browserID string
	testIDs   map[string]bool
}

type AgentClient struct {
	conn        *websocket.Conn
	ID          string
	EndpointID  string
	IPCReady    bool
	ConnectedAt string
	LastSeenAt  string
}

type ObserverRecord struct {
	ID        string
	Name      string
	LastSeen  *string
	Lat       *float64
	Lon       *float64
	UpdatedAt string
	Source    string
}

func main() {
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(cfg.Service.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}
	rt, err := NewRuntime(cfg, store)
	if err != nil {
		log.Fatal(err)
	}
	rt.Start()

	mux := http.NewServeMux()
	rt.routes(mux)
	serveStatic(mux, frontendFS())

	host := getenv("HOST", "0.0.0.0")
	port := getenv("PORT", "3000")
	addr := host + ":" + port
	log.Printf("Hopback Go listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, withCORS(mux)))
}

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Service.Name == "" {
		cfg.Service.Name = "Hopback"
	}
	if cfg.Service.DatabasePath == "" {
		cfg.Service.DatabasePath = "data/hopback.sqlite"
	}
	if cfg.Service.TestTTLMinutes == 0 {
		cfg.Service.TestTTLMinutes = 20
	}
	if len(cfg.CoreScope.URLs) == 0 {
		cfg.CoreScope.URLs = []string{"wss://analyzer.meshcore.cz", "wss://mc.pp0.co"}
	}
	if len(cfg.CoreScope.NodeAPIURLs) == 0 {
		for _, u := range cfg.CoreScope.URLs {
			cfg.CoreScope.NodeAPIURLs = append(cfg.CoreScope.NodeAPIURLs, wsToHTTP(u)+"/api/nodes?limit=2000&offset=0")
		}
	}
	if len(cfg.CoreScope.ObserverAPIURLs) == 0 {
		for _, u := range cfg.CoreScope.URLs {
			cfg.CoreScope.ObserverAPIURLs = append(cfg.CoreScope.ObserverAPIURLs, wsToHTTP(u)+"/api/observers")
		}
	}
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].PublicKey = strings.ToLower(cfg.Endpoints[i].PublicKey)
		if cfg.Endpoints[i].Region == "" {
			if cfg.Endpoints[i].Location != nil && cfg.Endpoints[i].Location.Label != "" {
				cfg.Endpoints[i].Region = cfg.Endpoints[i].Location.Label
			} else if cfg.Endpoints[i].Host != "" {
				cfg.Endpoints[i].Region = cfg.Endpoints[i].Host
			} else {
				cfg.Endpoints[i].Region = "MeshCore"
			}
		}
		if cfg.Endpoints[i].PrivateKey == "" {
			cfg.Endpoints[i].PrivateKey = cfg.Service.PrivateKey
		}
	}
	if cfg.Service.AgentSecret == "" {
		return cfg, errors.New("service.agentSecret is required")
	}
	return cfg, nil
}

func wsToHTTP(u string) string {
	u = strings.TrimRight(u, "/")
	u = strings.TrimPrefix(u, "wss://")
	u = strings.TrimPrefix(u, "ws://")
	if strings.HasPrefix(u, "localhost") || strings.HasPrefix(u, "127.") {
		return "http://" + u
	}
	return "https://" + u
}

func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	return s, s.migrate()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	create table if not exists tests (
		id text primary key,
		browser_id text not null,
		user_public_key text not null,
		endpoint_id text not null,
		endpoint_name text not null,
		endpoint_region text not null,
		endpoint_public_key text not null,
		code text not null,
		status text not null,
		qr_payload text not null,
		outbound_seen_at text,
		outbound_endpoint_seen_at text,
		outbound_ack_seen_at text,
		reply_broadcast_at text,
		return_seen_at text,
		reply_ack_seen_at text,
		reply_endpoint_ack_at text,
		outbound_hash text,
		outbound_ack_hash text,
		outbound_ack_crc_hex text,
		return_hash text,
		reply_hash text,
		reply_ack_hash text,
		reply_ack_crc_hex text,
		outbound_hex text,
		outbound_ack_hex text,
		return_hex text,
		reply_hex text,
		reply_ack_hex text,
		reply_status text,
		created_at text not null,
		updated_at text not null,
		expires_at text not null
	);
	create table if not exists observations (
		id integer primary key autoincrement,
		test_id text not null references tests(id) on delete cascade,
		direction text not null,
		source text not null,
		packet_hash text not null,
		observer_id text,
		observer_name text,
		hop_count integer not null,
		path_json text not null,
		path_keys_json text not null default '[]',
		decoded_type text,
		created_at text not null
	);
	create table if not exists nodes (
		public_key text primary key,
		name text not null,
		short_hash text not null,
		node_type integer,
		lat real,
		lon real,
		updated_at text not null,
		source text not null
	);
	create index if not exists tests_browser_idx on tests(browser_id, created_at desc);
	create index if not exists tests_active_idx on tests(status, expires_at);
	create index if not exists nodes_short_hash_idx on nodes(short_hash);
	create index if not exists nodes_name_idx on nodes(name);
	create index if not exists observations_packet_observer_path_idx on observations(test_id, packet_hash, direction, source, observer_id, path_json);
	`)
	return err
}

func NewRuntime(cfg Config, store *Store) (*Runtime, error) {
	rt := &Runtime{
		cfg:       cfg,
		store:     store,
		upgrader:  websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		packetCh:  make(chan PacketEvent, packetQueueSize),
		browsers:  map[*BrowserClient]bool{},
		agents:    map[string]*AgentClient{},
		analyzers: map[string]AnalyzerState{},
		observers: map[string]ObserverRecord{},
		active:    map[string]*ActiveTest{},
		seen:      map[string]time.Time{},
	}
	tests, err := store.ListActiveTests(cfg.Endpoints)
	if err != nil {
		return nil, err
	}
	for i := range tests {
		rt.registerActive(&tests[i])
	}
	return rt, nil
}

func (rt *Runtime) Start() {
	for i := 0; i < max(2, min(8, len(rt.cfg.Endpoints)*2)); i++ {
		go rt.packetWorker()
	}
	go rt.cleanupLoop()
	go rt.refreshNodesLoop()
	go rt.refreshObserversLoop()
	for _, u := range rt.cfg.CoreScope.URLs {
		go rt.connectCoreScope(u)
	}
	if rt.cfg.Service.Verbose {
		log.Printf("[runtime] Hopback Go started")
	}
}

func (rt *Runtime) cleanupLoop() {
	t := time.NewTicker(time.Minute)
	for range t.C {
		rt.mu.Lock()
		cut := time.Now().Add(-30 * time.Minute)
		for id, active := range rt.active {
			if tm, err := time.Parse(time.RFC3339, active.Test.ExpiresAt); err == nil && tm.Before(cut) {
				delete(rt.active, id)
			}
		}
		rt.rebuildIndexLocked()
		now := time.Now()
		for key, seen := range rt.seen {
			if now.Sub(seen) > 10*time.Minute || len(rt.seen) > maxPacketSeen {
				delete(rt.seen, key)
			}
		}
		rt.mu.Unlock()
	}
}

func (rt *Runtime) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", rt.handleStatus)
	mux.HandleFunc("GET /api/nodes", rt.handleNodes)
	mux.HandleFunc("GET /api/tests", rt.handleTestsList)
	mux.HandleFunc("POST /api/tests", rt.handleCreateTest)
	mux.HandleFunc("POST /api/tests/meta", rt.handleTestMetas)
	mux.HandleFunc("GET /api/tests/{id}", rt.handleGetTest)
	mux.HandleFunc("GET /ws", rt.handleBrowserWS)
	mux.HandleFunc("GET /agent", rt.handleAgentWS)
}

func (rt *Runtime) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, rt.Status())
}

func (rt *Runtime) Status() RuntimeStatus {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	analyzers := make([]AnalyzerState, 0, len(rt.cfg.CoreScope.URLs))
	for _, u := range rt.cfg.CoreScope.URLs {
		st := rt.analyzers[u]
		if st.URL == "" {
			st = AnalyzerState{URL: u, State: "closed"}
		}
		analyzers = append(analyzers, st)
	}
	agents := make([]AgentStatus, 0, len(rt.agents))
	for _, a := range rt.agents {
		agents = append(agents, AgentStatus{ID: a.ID, EndpointID: a.EndpointID, IPCReady: a.IPCReady, ConnectedAt: a.ConnectedAt, LastSeenAt: a.LastSeenAt})
	}
	eps := make([]EndpointStatus, 0, len(rt.cfg.Endpoints))
	for _, ep := range rt.cfg.Endpoints {
		var agent *AgentClient
		for _, a := range rt.agents {
			if a.EndpointID == ep.ID {
				agent = a
				break
			}
		}
		st := EndpointStatus{ID: ep.ID, Name: ep.Name, Region: ep.Region, PublicKey: ep.PublicKey, Host: ep.Host, Location: ep.Location, AgentID: ep.AgentID}
		if agent != nil {
			st.Connected = true
			st.Ready = agent.IPCReady
			st.IPCReady = agent.IPCReady
			st.AgentID = agent.ID
			st.LastSeenAt = agent.LastSeenAt
		}
		eps = append(eps, st)
	}
	activeObs := 0
	cut := time.Now().Add(-5 * time.Minute)
	for _, o := range rt.observers {
		if o.LastSeen != nil {
			if tm, err := time.Parse(time.RFC3339, *o.LastSeen); err == nil && tm.After(cut) {
				activeObs++
			}
		}
	}
	nodes, _ := rt.store.CountNodes()
	return RuntimeStatus{Analyzers: analyzers, Endpoints: eps, Agents: agents, Nodes: nodes, Observers: len(rt.observers), ActiveObservers: activeObs, ActiveTests: len(rt.active), Verbose: rt.cfg.Service.Verbose}
}

func (rt *Runtime) handleNodes(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 200, 1, 2000)
	q := r.URL.Query().Get("q")
	var updatedAfter *string
	if d, _ := strconv.Atoi(r.URL.Query().Get("recentDays")); d > 0 {
		v := time.Now().Add(-time.Duration(d) * 24 * time.Hour).Format(time.RFC3339)
		updatedAfter = &v
	}
	nodes, err := rt.store.ListNodes(q, limit, updatedAfter)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"endpoints": rt.cfg.Endpoints, "nodes": nodes, "configuredEndpointCount": len(rt.cfg.Endpoints)})
}

func (rt *Runtime) handleTestMetas(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	tests, err := rt.store.GetTestMetas(body.IDs, rt.cfg.Endpoints)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"tests": tests})
}

func (rt *Runtime) handleTestsList(w http.ResponseWriter, r *http.Request) {
	browserID := r.URL.Query().Get("browserId")
	limit := intParam(r, "limit", 30, 1, 100)
	offset := intParam(r, "offset", 0, 0, 100000)
	if browserID == "" {
		writeJSON(w, map[string]any{"tests": []Test{}, "total": 0, "limit": limit, "offset": offset})
		return
	}
	tests, err := rt.store.ListTestsForBrowser(browserID, limit, offset, rt.cfg.Endpoints)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	total, _ := rt.store.CountTestsForBrowser(browserID)
	writeJSON(w, map[string]any{"tests": tests, "total": total, "limit": limit, "offset": offset})
}

func (rt *Runtime) handleGetTest(w http.ResponseWriter, r *http.Request) {
	test, err := rt.getTest(r.PathValue("id"))
	if err != nil || test == nil {
		writeJSONStatus(w, 404, map[string]string{"message": "Test not found"})
		return
	}
	if test.Status != "expired" {
		test.QRDataURL = qrDataURL(test.QRPayload)
	}
	writeJSON(w, map[string]any{"test": test})
}

func (rt *Runtime) handleCreateTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserPublicKey string `json:"userPublicKey"`
		EndpointID    string `json:"endpointId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONStatus(w, 400, map[string]string{"message": "Invalid JSON"})
		return
	}
	body.UserPublicKey = strings.ToLower(strings.TrimSpace(body.UserPublicKey))
	if !isHex(body.UserPublicKey, 32) {
		writeJSONStatus(w, 400, map[string]string{"message": "User public key must be 64 hex characters"})
		return
	}
	ep := rt.endpoint(body.EndpointID)
	if ep == nil {
		writeJSONStatus(w, 400, map[string]string{"message": "Choose an endpoint"})
		return
	}
	if !rt.endpointReady(ep.ID) {
		writeJSONStatus(w, 503, map[string]string{"message": ep.Name + " endpoint agent is offline. Start its agent before testing."})
		return
	}
	code := rt.unusedCode()
	if code == "" {
		writeJSONStatus(w, 503, map[string]string{"message": "Could not allocate a unique test code"})
		return
	}
	qrPayload := buildQRPayload(*ep, code)
	now := time.Now().UTC()
	test := &Test{
		ID: code, BrowserID: randomID(), UserPublicKey: body.UserPublicKey,
		EndpointID: ep.ID, EndpointName: ep.Name, EndpointRegion: ep.Region, EndpointPublicKey: ep.PublicKey, EndpointLocation: ep.Location,
		Code: code, Status: "waiting", QRPayload: qrPayload, CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(time.Duration(rt.cfg.Service.TestTTLMinutes) * time.Minute).Format(time.RFC3339), Observations: []Observation{}, Nodes: map[string]NodeRef{},
	}
	if err := rt.store.CreateTest(test); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rt.mu.Lock()
	rt.registerActiveLocked(test)
	rt.mu.Unlock()
	test.QRDataURL = qrDataURL(qrPayload)
	writeJSON(w, map[string]any{"test": test})
}

func (rt *Runtime) endpoint(id string) *Endpoint {
	for i := range rt.cfg.Endpoints {
		if rt.cfg.Endpoints[i].ID == id {
			return &rt.cfg.Endpoints[i]
		}
	}
	return nil
}

func (rt *Runtime) endpointReady(id string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, a := range rt.agents {
		if a.EndpointID == id && a.IPCReady {
			return true
		}
	}
	return false
}

func (rt *Runtime) unusedCode() string {
	for i := 0; i < 10; i++ {
		code := fmt.Sprintf("%06x", rand.Uint32()&0xffffff)
		exists, _ := rt.store.TestExists(code)
		if !exists {
			return code
		}
	}
	return ""
}

func (rt *Runtime) getTest(id string) (*Test, error) {
	rt.mu.RLock()
	if active := rt.active[id]; active != nil {
		cp := *active.Test
		rt.mu.RUnlock()
		return rt.decorate(&cp), nil
	}
	rt.mu.RUnlock()
	t, err := rt.store.GetTest(id, rt.cfg.Endpoints)
	if err != nil || t == nil {
		return t, err
	}
	return rt.decorate(t), nil
}

func (rt *Runtime) decorate(t *Test) *Test {
	if t.Nodes == nil {
		t.Nodes = map[string]NodeRef{}
	}
	t.Status = deriveStatus(t)
	t.DeliveryPaths = map[string][]any{"outbound": {}, "return": {}}
	t.PropagationMap = map[string][]any{"points": {}, "segments": {}}
	return t
}

func (rt *Runtime) registerActive(t *Test) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.registerActiveLocked(t)
}

func (rt *Runtime) registerActiveLocked(t *Test) {
	t.Status = deriveStatus(t)
	active := &ActiveTest{Test: t, Keys: map[string]bool{}}
	for _, o := range t.Observations {
		active.Keys[observationKey(o)] = true
	}
	rt.active[t.ID] = active
	rt.rebuildIndexLocked()
}

func (rt *Runtime) rebuildIndexLocked() {
	idx := ActiveIndex{ByPair: map[string][]*ActiveTest{}, ByCRC: map[string]PendingMatch{}}
	for _, active := range rt.active {
		t := active.Test
		if len(t.EndpointPublicKey) >= 2 && len(t.UserPublicKey) >= 2 {
			ep := strings.ToLower(t.EndpointPublicKey[:2])
			user := strings.ToLower(t.UserPublicKey[:2])
			idx.ByPair[ep+"|"+user] = append(idx.ByPair[ep+"|"+user], active)
			idx.ByPair[user+"|"+ep] = append(idx.ByPair[user+"|"+ep], active)
		}
		if t.OutboundAckCRCHex != nil {
			idx.ByCRC[strings.ToLower(*t.OutboundAckCRCHex)] = PendingMatch{TestID: t.ID, Direction: "outbound", DecodedType: "ACK"}
		}
		if t.ReplyAckCRCHex != nil {
			idx.ByCRC[strings.ToLower(*t.ReplyAckCRCHex)] = PendingMatch{TestID: t.ID, Direction: "return", DecodedType: "ACK"}
		}
	}
	rt.index = idx
}

func (rt *Runtime) handleBrowserWS(w http.ResponseWriter, r *http.Request) {
	conn, err := rt.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &BrowserClient{conn: conn, browserID: r.URL.Query().Get("browserId"), testIDs: map[string]bool{}}
	if client.browserID == "" {
		client.browserID = "anonymous"
	}
	rt.mu.Lock()
	rt.browsers[client] = true
	rt.mu.Unlock()
	_ = conn.WriteJSON(map[string]any{"type": "hello", "status": rt.Status()})
	for {
		var msg struct {
			Type   string `json:"type"`
			TestID string `json:"testId"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == "subscribe" && msg.TestID != "" {
			client.testIDs[msg.TestID] = true
			if t, _ := rt.getTest(msg.TestID); t != nil {
				_ = conn.WriteJSON(map[string]any{"type": "testUpdated", "test": t})
			}
		}
	}
	rt.mu.Lock()
	delete(rt.browsers, client)
	rt.mu.Unlock()
	_ = conn.Close()
}

func (rt *Runtime) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	if !rt.authorized(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := rt.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := r.URL.Query().Get("id")
	if id == "" {
		id = randomID()
	}
	agent := &AgentClient{conn: conn, ID: id, EndpointID: r.URL.Query().Get("endpointId"), ConnectedAt: now, LastSeenAt: now}
	rt.mu.Lock()
	rt.agents[id] = agent
	rt.mu.Unlock()
	_ = conn.WriteJSON(map[string]any{"type": "hello", "id": id, "status": rt.Status()})
	rt.publishStatus()
	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			break
		}
		agent.LastSeenAt = time.Now().UTC().Format(time.RFC3339)
		rt.handleAgentMessage(agent, raw)
	}
	rt.mu.Lock()
	delete(rt.agents, id)
	rt.mu.Unlock()
	rt.publishStatus()
	_ = conn.Close()
}

func (rt *Runtime) authorized(r *http.Request) bool {
	if r.URL.Query().Get("secret") == rt.cfg.Service.AgentSecret {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+rt.cfg.Service.AgentSecret
}

func (rt *Runtime) handleAgentMessage(agent *AgentClient, raw json.RawMessage) {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		return
	}
	switch base.Type {
	case "hello":
		var msg struct {
			ID         string `json:"id"`
			EndpointID string `json:"endpointId"`
			IPCReady   bool   `json:"ipcReady"`
		}
		_ = json.Unmarshal(raw, &msg)
		rt.mu.Lock()
		if msg.ID != "" && msg.ID != agent.ID {
			delete(rt.agents, agent.ID)
			agent.ID = msg.ID
			rt.agents[agent.ID] = agent
		}
		if msg.EndpointID != "" {
			agent.EndpointID = msg.EndpointID
		}
		agent.IPCReady = msg.IPCReady
		rt.mu.Unlock()
		rt.publishStatus()
		if agent.IPCReady {
			rt.retryPendingReplies(agent)
		}
	case "observedPacket":
		var msg struct {
			RawHex    string   `json:"rawHex"`
			Timestamp *string  `json:"timestamp"`
			RSSI      *float64 `json:"rssi"`
			SNR       *float64 `json:"snr"`
		}
		_ = json.Unmarshal(raw, &msg)
		rt.enqueuePacket(PacketEvent{Source: "agent:" + agent.ID, RawHex: msg.RawHex, Timestamp: msg.Timestamp, RSSI: msg.RSSI, SNR: msg.SNR})
	case "sendRawResult":
		var msg struct {
			TestID     string `json:"testId"`
			PacketRole string `json:"packetRole"`
			RawHex     string `json:"rawHex"`
			OK         bool   `json:"ok"`
			Error      string `json:"error"`
		}
		_ = json.Unmarshal(raw, &msg)
		rt.handleSendResult(msg.TestID, msg.PacketRole, msg.OK, msg.Error)
	}
}

func (rt *Runtime) enqueuePacket(packet PacketEvent) {
	rt.mu.RLock()
	active := len(rt.active)
	rt.mu.RUnlock()
	if active == 0 {
		return
	}
	select {
	case rt.packetCh <- packet:
	default:
		if strings.HasPrefix(packet.Source, "agent:") {
			rt.packetCh <- packet
		}
	}
}

func (rt *Runtime) packetWorker() {
	for p := range rt.packetCh {
		rt.processPacket(p)
	}
}

func (rt *Runtime) processPacket(event PacketEvent) {
	raw, err := hex.DecodeString(strings.TrimSpace(event.RawHex))
	if err != nil {
		return
	}
	pkt, err := meshpkt.DecodePacket(raw)
	if err != nil {
		return
	}
	hash := event.Hash
	if hash == "" {
		h := meshpkt.ContentHash(pkt)
		hash = hex.EncodeToString(h[:])
		event.Hash = hash
	}
	rt.mu.Lock()
	if _, ok := rt.seen[event.Source+"|"+hash]; ok {
		rt.mu.Unlock()
		return
	}
	rt.seen[event.Source+"|"+hash] = time.Now()
	rt.mu.Unlock()

	if pkt.Type == meshpkt.PayloadAck {
		crc, err := meshpkt.DecodeAckPayload(pkt.Payload)
		if err == nil {
			rt.mu.RLock()
			match, ok := rt.index.ByCRC[fmt.Sprintf("%08x", crc)]
			rt.mu.RUnlock()
			if ok {
				rt.recordPacket(event, match.TestID, match.Direction, match.DecodedType, &pkt, "")
			}
		}
		return
	}

	candidates := rt.candidates(pkt)
	if len(candidates) == 0 {
		return
	}
	for _, active := range candidates {
		decoded := rt.tryDecode(pkt, active.Test)
		if decoded == nil {
			continue
		}
		if decoded.AckCRCHex != "" {
			rt.mu.RLock()
			t := rt.active[active.Test.ID].Test
			replyAck := strVal(t.ReplyAckCRCHex)
			outboundAck := strVal(t.OutboundAckCRCHex)
			rt.mu.RUnlock()
			if decoded.AckCRCHex == replyAck {
				rt.recordPacket(event, active.Test.ID, "return", decoded.Type, &pkt, decoded.Text)
				return
			}
			if decoded.AckCRCHex == outboundAck {
				rt.recordPacket(event, active.Test.ID, "outbound", decoded.Type, &pkt, decoded.Text)
				return
			}
		}
		if strings.Contains(decoded.Text, active.Test.Code) || strings.Contains(decoded.DataHex, active.Test.Code) || strings.Contains(strings.ToLower(event.RawHex), strings.ToLower(active.Test.Code)) {
			dir := decoded.Direction
			if strings.Contains(decoded.Text, "Hopback "+active.Test.Code+" received by") || (isAckType(decoded.Type) && active.Test.ReplyBroadcastAt != nil) {
				dir = "return"
			}
			recorded := rt.recordPacket(event, active.Test.ID, dir, decoded.Type, &pkt, decoded.Text)
			if recorded && dir == "outbound" && autoReply(rt.cfg) && strings.HasPrefix(event.Source, "agent:") {
				if t, _ := rt.getTest(active.Test.ID); t != nil {
					rt.sendReply(t)
				}
			}
			return
		}
	}
}

func (rt *Runtime) candidates(pkt meshpkt.Packet) []*ActiveTest {
	if len(pkt.Payload) < 2 {
		return nil
	}
	key := fmt.Sprintf("%02x|%02x", pkt.Payload[0], pkt.Payload[1])
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return append([]*ActiveTest(nil), rt.index.ByPair[key]...)
}

type DecodedPayload struct {
	Type      string
	Text      string
	DataHex   string
	AckCRCHex string
	Direction string
}

func (rt *Runtime) tryDecode(pkt meshpkt.Packet, t *Test) *DecodedPayload {
	ep := rt.endpoint(t.EndpointID)
	if ep == nil || ep.PrivateKey == "" {
		return nil
	}
	priv := ep.PrivateKey
	switch pkt.Type {
	case meshpkt.PayloadTxtMsg:
		if len(priv) == 128 {
			if d, err := meshpkt.DecodeDirectTextPayloadFromExpanded(pkt.Payload, priv, t.UserPublicKey); err == nil {
				return &DecodedPayload{Type: "TXT_MSG_IDENTITY", Text: d.Text, Direction: "outbound"}
			}
		}
		if d, err := meshpkt.DecodeDirectTextPayloadFromIdentity(pkt.Payload, priv[:min(64, len(priv))], t.UserPublicKey); err == nil {
			return &DecodedPayload{Type: "TXT_MSG_IDENTITY", Text: d.Text, Direction: "outbound"}
		}
	case meshpkt.PayloadPath:
		var d meshpkt.ReturnedPath
		var err error
		if len(priv) == 128 {
			d, err = meshpkt.DecodePathPayloadFromExpanded(pkt.Payload, priv, t.UserPublicKey)
		} else {
			d, err = meshpkt.DecodePathPayloadFromIdentity(pkt.Payload, priv[:min(64, len(priv))], t.UserPublicKey)
		}
		if err == nil {
			out := &DecodedPayload{Type: "PATH", DataHex: hex.EncodeToString(d.Extra), Direction: "outbound"}
			if len(d.Extra) >= 4 {
				out.AckCRCHex = fmt.Sprintf("%08x", binary.LittleEndian.Uint32(d.Extra[:4]))
			}
			return out
		}
	case meshpkt.PayloadResponse:
		// RESPONSE is rare for Hopback tests; skip expensive broad matching until
		// an ACK CRC or text-bearing packet points at this test.
		return nil
	}
	return nil
}

func (rt *Runtime) recordPacket(event PacketEvent, testID, direction, typ string, pkt *meshpkt.Packet, text string) bool {
	rt.mu.Lock()
	active := rt.active[testID]
	if active == nil {
		rt.mu.Unlock()
		return false
	}
	now := time.Now().UTC().Format(time.RFC3339)
	path := event.Path
	if len(path) == 0 && pkt != nil {
		for _, hop := range pkt.Hops() {
			path = append(path, hex.EncodeToString(hop))
		}
	}
	pathKeys := make([]*string, len(path))
	obs := Observation{ID: -int64(len(active.Test.Observations) + 1), Direction: direction, Source: event.Source, PacketHash: event.Hash, ObserverID: event.ObserverID, ObserverName: event.ObserverName, HopCount: len(path), Path: path, PathKeys: pathKeys, DecodedType: ptr(typ), CreatedAt: now}
	key := observationKey(obs)
	if active.Keys[key] {
		rt.mu.Unlock()
		return false
	}
	active.Keys[key] = true
	active.Test.Observations = append([]Observation{obs}, active.Test.Observations...)
	rt.applyFactsLocked(active.Test, obs, event.RawHex)
	rt.rebuildIndexLocked()
	testCopy := *active.Test
	rt.mu.Unlock()

	_ = rt.store.AddObservation(testID, obs)
	_ = rt.store.UpdateFacts(testID, activeFacts(active.Test))
	rt.publishObservation(rt.decorate(&testCopy), obs)
	return true
}

func (rt *Runtime) applyFactsLocked(t *Test, obs Observation, rawHex string) {
	now := obs.CreatedAt
	isEndpoint := strings.HasPrefix(obs.Source, "agent:")
	if obs.ObserverID != nil && strings.EqualFold(*obs.ObserverID, t.EndpointPublicKey) {
		isEndpoint = true
	}
	if obs.Direction == "outbound" {
		if isAckType(strVal(obs.DecodedType)) {
			if t.OutboundAckSeenAt == nil {
				t.OutboundAckSeenAt, t.OutboundAckHash, t.OutboundAckHex = &now, &obs.PacketHash, &rawHex
				t.ReplyStatus = ptr("Outbound ACK observed")
			}
		} else if t.OutboundSeenAt == nil {
			t.OutboundSeenAt, t.OutboundHash, t.OutboundHex = &now, &obs.PacketHash, &rawHex
		}
		if !isAckType(strVal(obs.DecodedType)) && isEndpoint && t.OutboundEndpointSeenAt == nil {
			t.OutboundEndpointSeenAt = &now
			t.OutboundHash, t.OutboundHex = &obs.PacketHash, &rawHex
		}
	}
	if obs.Direction == "return" {
		if isAckType(strVal(obs.DecodedType)) {
			if t.ReplyAckSeenAt == nil {
				t.ReplyAckSeenAt, t.ReplyAckHash, t.ReplyAckHex = &now, &obs.PacketHash, &rawHex
				t.ReplyStatus = ptr("Reply ACK observed")
			}
			if isEndpoint && t.ReplyEndpointAckAt == nil {
				t.ReplyEndpointAckAt = &now
				t.ReplyStatus = ptr("Reply ACK arrived at endpoint")
			}
		} else if t.ReturnSeenAt == nil {
			t.ReturnSeenAt, t.ReturnHash, t.ReturnHex = &now, &obs.PacketHash, &rawHex
			t.ReplyStatus = ptr("Reply packet observed")
		}
	}
	t.Status = deriveStatus(t)
	t.UpdatedAt = now
}

func (rt *Runtime) sendReply(t *Test) {
	if t.OutboundHex == nil || t.ReplyEndpointAckAt != nil {
		return
	}
	agent := rt.agentForEndpoint(t.EndpointID)
	if agent == nil || !agent.IPCReady {
		rt.setReplyStatus(t.ID, "Outbound reached endpoint, but no endpoint agent with IPC is connected")
		return
	}
	packets, err := rt.buildReplyPackets(t)
	if err != nil {
		rt.setReplyStatus(t.ID, err.Error())
		return
	}
	for _, p := range packets {
		if p.Role == "outboundAck" && t.OutboundAckHash != nil {
			continue
		}
		if p.Role == "replyMessage" && t.ReplyBroadcastAt != nil {
			continue
		}
		_ = agent.conn.WriteJSON(map[string]any{"type": "sendRaw", "testId": t.ID, "packetRole": p.Role, "rawHex": p.Hex})
		rt.noteQueuedPacket(t.ID, p)
		return
	}
}

type OutPacket struct {
	Role      string
	Hex       string
	Hash      string
	AckCRCHex string
}

func (rt *Runtime) buildReplyPackets(t *Test) ([]OutPacket, error) {
	ep := rt.endpoint(t.EndpointID)
	if ep == nil || ep.PrivateKey == "" || t.OutboundHex == nil {
		return nil, errors.New("endpoint private key or outbound packet is missing")
	}
	raw, err := hex.DecodeString(*t.OutboundHex)
	if err != nil {
		return nil, err
	}
	pkt, err := meshpkt.DecodePacket(raw)
	if err != nil {
		return nil, err
	}
	decoded := rt.tryDecode(pkt, t)
	if decoded == nil || decoded.Text == "" {
		return nil, errors.New("cannot build reply: outbound packet was not decoded")
	}
	text := decoded.Text
	ts := uint32(time.Now().Unix())
	if d, err := decodeOutboundText(pkt, ep.PrivateKey, t.UserPublicKey); err == nil {
		ts = uint32(d.Timestamp.Unix())
		text = d.Text
	}
	userPub, _ := hex.DecodeString(t.UserPublicKey)
	crc := meshpkt.TextAckCRC(ts, 1, text, userPub)
	out := []OutPacket{}
	if pkt.HopCount() > 0 {
		var ack []byte
		if len(ep.PrivateKey) == 128 {
			ack, err = meshpkt.PathTextAckReturnPacketFromExpanded(ep.PrivateKey, ep.PublicKey, t.UserPublicKey, ts, 1, text, pkt.Path, meshpkt.WithPathHashSize(pkt.PathHashSize))
		} else {
			var seed [32]byte
			b, _ := hex.DecodeString(ep.PrivateKey[:64])
			copy(seed[:], b)
			var peer [32]byte
			copy(peer[:], userPub)
			ack, err = meshpkt.PathTextAckReturnPacketFromIdentity(seed, peer, ts, 1, text, pkt.Path, meshpkt.WithPathHashSize(pkt.PathHashSize))
		}
		if err == nil {
			out = append(out, rt.outPacket("outboundAck", ack, fmt.Sprintf("%08x", crc)))
		}
	} else {
		ack, err := meshpkt.TextAckPacket(ts, 1, text, userPub)
		if err == nil {
			out = append(out, rt.outPacket("outboundAck", ack, fmt.Sprintf("%08x", crc)))
		}
	}
	replyText := "Hopback " + t.Code + " received by " + t.EndpointName
	var reply []byte
	if len(ep.PrivateKey) == 128 {
		reply, err = meshpkt.DirectTextPacketFromExpanded(ep.PrivateKey, ep.PublicKey, t.UserPublicKey, replyText, time.Now(), 1)
	} else {
		var seed [32]byte
		b, _ := hex.DecodeString(ep.PrivateKey[:64])
		copy(seed[:], b)
		var peer [32]byte
		copy(peer[:], userPub)
		reply, err = meshpkt.DirectTextPacketFromIdentity(seed, peer, replyText, time.Now(), 1)
	}
	if err == nil {
		replyCRC := meshpkt.TextAckCRC(uint32(time.Now().Unix()), 1, replyText, mustHex(ep.PublicKey))
		out = append(out, rt.outPacket("replyMessage", reply, fmt.Sprintf("%08x", replyCRC)))
	}
	return out, nil
}

func (rt *Runtime) outPacket(role string, raw []byte, crc string) OutPacket {
	h := ""
	if pkt, err := meshpkt.DecodePacket(raw); err == nil {
		ch := meshpkt.ContentHash(pkt)
		h = hex.EncodeToString(ch[:])
	}
	return OutPacket{Role: role, Hex: hex.EncodeToString(raw), Hash: h, AckCRCHex: crc}
}

func decodeOutboundText(pkt meshpkt.Packet, priv, userPub string) (meshpkt.DirectText, error) {
	if len(priv) == 128 {
		return meshpkt.DecodeDirectTextPayloadFromExpanded(pkt.Payload, priv, userPub)
	}
	return meshpkt.DecodeDirectTextPayloadFromIdentity(pkt.Payload, priv[:min(64, len(priv))], userPub)
}

func (rt *Runtime) noteQueuedPacket(testID string, p OutPacket) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	active := rt.active[testID]
	if active == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if p.Role == "outboundAck" {
		active.Test.OutboundAckHash, active.Test.OutboundAckCRCHex, active.Test.OutboundAckHex = &p.Hash, &p.AckCRCHex, &p.Hex
		active.Test.ReplyStatus = ptr("Outbound ACK queued")
	} else {
		active.Test.ReplyHash, active.Test.ReplyAckCRCHex, active.Test.ReplyHex = &p.Hash, &p.AckCRCHex, &p.Hex
		active.Test.ReplyStatus = ptr("Reply queued")
	}
	active.Test.UpdatedAt = now
	rt.rebuildIndexLocked()
	_ = rt.store.UpdateFacts(testID, activeFacts(active.Test))
	rt.publishTestLocked(active.Test)
}

func (rt *Runtime) handleSendResult(testID, role string, ok bool, errText string) {
	rt.mu.Lock()
	active := rt.active[testID]
	if active == nil {
		rt.mu.Unlock()
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if !ok {
		active.Test.ReplyStatus = ptr(firstNonEmpty(errText, "Agent failed to send packet"))
	} else if role == "outboundAck" {
		active.Test.ReplyStatus = ptr("Outbound ACK handed to MeshCore IPC")
	} else {
		active.Test.ReplyBroadcastAt = &now
		active.Test.ReplyStatus = ptr("Reply handed to MeshCore IPC")
	}
	active.Test.UpdatedAt = now
	testCopy := *active.Test
	rt.mu.Unlock()
	_ = rt.store.UpdateFacts(testID, activeFacts(&testCopy))
	rt.publishTest(rt.decorate(&testCopy))
	if ok && role == "outboundAck" {
		rt.sendReply(&testCopy)
	}
}

func (rt *Runtime) setReplyStatus(testID, status string) {
	rt.mu.Lock()
	if active := rt.active[testID]; active != nil {
		active.Test.ReplyStatus = &status
		active.Test.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		rt.publishTestLocked(active.Test)
		_ = rt.store.UpdateFacts(testID, activeFacts(active.Test))
	}
	rt.mu.Unlock()
}

func (rt *Runtime) agentForEndpoint(endpointID string) *AgentClient {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, a := range rt.agents {
		if a.EndpointID == endpointID {
			return a
		}
	}
	return nil
}

func (rt *Runtime) retryPendingReplies(agent *AgentClient) {
	rt.mu.RLock()
	tests := []*Test{}
	for _, active := range rt.active {
		t := active.Test
		if t.EndpointID == agent.EndpointID && t.OutboundEndpointSeenAt != nil && (t.OutboundAckHash == nil || t.ReplyBroadcastAt == nil) && t.ReplyEndpointAckAt == nil {
			cp := *t
			tests = append(tests, &cp)
		}
	}
	rt.mu.RUnlock()
	for _, t := range tests {
		rt.sendReply(t)
	}
}

func (rt *Runtime) publishStatus() {
	payload := map[string]any{"type": "status", "status": rt.Status()}
	rt.mu.RLock()
	clients := make([]*BrowserClient, 0, len(rt.browsers))
	for c := range rt.browsers {
		clients = append(clients, c)
	}
	rt.mu.RUnlock()
	for _, c := range clients {
		_ = c.conn.WriteJSON(payload)
	}
}

func (rt *Runtime) publishTest(t *Test) {
	rt.mu.RLock()
	clients := make([]*BrowserClient, 0, len(rt.browsers))
	for c := range rt.browsers {
		if c.browserID == t.BrowserID || c.testIDs[t.ID] {
			clients = append(clients, c)
		}
	}
	rt.mu.RUnlock()
	for _, c := range clients {
		_ = c.conn.WriteJSON(map[string]any{"type": "testUpdated", "test": t})
	}
}

func (rt *Runtime) publishTestLocked(t *Test) {
	go rt.publishTest(rt.decorate(t))
}

func (rt *Runtime) publishObservation(t *Test, obs Observation) {
	rt.publishTest(t)
	rt.mu.RLock()
	clients := make([]*BrowserClient, 0, len(rt.browsers))
	for c := range rt.browsers {
		if c.browserID == t.BrowserID || c.testIDs[t.ID] {
			clients = append(clients, c)
		}
	}
	rt.mu.RUnlock()
	for _, c := range clients {
		_ = c.conn.WriteJSON(map[string]any{"type": "observation", "testId": t.ID, "test": t, "observation": obs})
	}
}

func (rt *Runtime) connectCoreScope(url string) {
	for {
		rt.setAnalyzer(url, "connecting", "")
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			rt.setAnalyzer(url, "error", err.Error())
			time.Sleep(3 * time.Second)
			continue
		}
		rt.setAnalyzer(url, "open", "")
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				rt.setAnalyzer(url, "closed", err.Error())
				break
			}
			rt.handleCoreScopeMessage(url, msg)
		}
		time.Sleep(3 * time.Second)
	}
}

func (rt *Runtime) setAnalyzer(url, state, errText string) {
	rt.mu.Lock()
	st := rt.analyzers[url]
	st.URL, st.State, st.LastError = url, state, errText
	rt.analyzers[url] = st
	rt.mu.Unlock()
}

func (rt *Runtime) handleCoreScopeMessage(source string, msg []byte) {
	var root map[string]any
	if json.Unmarshal(msg, &root) != nil || root["type"] != "packet" {
		return
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return
	}
	packet, _ := data["packet"].(map[string]any)
	if packet == nil {
		packet = data
	}
	raw := stringField(packet, "raw_hex", "rawHex")
	if raw == "" {
		return
	}
	rt.mu.Lock()
	st := rt.analyzers[source]
	st.URL, st.State, st.LastMessageAt = source, "open", time.Now().UTC().Format(time.RFC3339)
	rt.analyzers[source] = st
	rt.mu.Unlock()
	var payloadType *string
	if decoded, _ := data["decoded"].(map[string]any); decoded != nil {
		if header, _ := decoded["header"].(map[string]any); header != nil {
			if v := stringField(header, "payloadTypeName"); v != "" {
				payloadType = &v
			}
		}
	}
	rt.enqueuePacket(PacketEvent{
		Source: "corescope:" + source, RawHex: raw, Hash: stringField(packet, "hash"),
		ObserverID:   ptrIf(stringField(packet, "observer_id", "observerId")),
		ObserverName: ptrIf(stringField(packet, "observer_name", "observerName")),
		Path:         parsePath(packet["path_json"], packet["path"]), ResolvedPath: parseStringSlice(packet["resolved_path"]),
		PayloadType: payloadType, Original: data,
	})
}

func (rt *Runtime) refreshNodesLoop() {
	for {
		for _, url := range rt.cfg.CoreScope.NodeAPIURLs {
			rt.refreshNodes(url)
		}
		time.Sleep(5 * time.Minute)
	}
}

func (rt *Runtime) refreshNodes(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var payload any
	if json.NewDecoder(resp.Body).Decode(&payload) != nil {
		return
	}
	nodes := normalizeNodes(payload, url)
	_ = rt.store.UpsertNodes(nodes)
}

func (rt *Runtime) refreshObserversLoop() {
	for {
		for _, url := range rt.cfg.CoreScope.ObserverAPIURLs {
			rt.refreshObservers(url)
		}
		time.Sleep(5 * time.Minute)
	}
}

func (rt *Runtime) refreshObservers(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var payload any
	if json.NewDecoder(resp.Body).Decode(&payload) != nil {
		return
	}
	obs := normalizeObservers(payload, url)
	rt.mu.Lock()
	for _, o := range obs {
		rt.observers[o.ID] = o
	}
	rt.mu.Unlock()
}

func frontendFS() fs.FS {
	sub, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	return sub
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "content-type,authorization")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveStatic(mux *http.ServeMux, staticFS fs.FS) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if !staticFileExists(staticFS, name) {
			name = "index.html"
		}
		if !staticFileExists(staticFS, name) {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(staticFS, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

func staticFileExists(staticFS fs.FS, name string) bool {
	st, err := fs.Stat(staticFS, name)
	return err == nil && !st.IsDir()
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, 200, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func intParam(r *http.Request, name string, fallback, minv, maxv int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return min(max(n, minv), maxv)
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func autoReply(cfg Config) bool {
	return cfg.Service.AutoReply == nil || *cfg.Service.AutoReply
}

func ptr[T any](v T) *T { return &v }

func ptrIf(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isHex(value string, bytes int) bool {
	if len(value) != bytes*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mustHex(v string) []byte {
	b, _ := hex.DecodeString(v)
	return b
}

func randomID() string {
	return fmt.Sprintf("%08x%08x", rand.Uint32(), rand.Uint32())
}

func buildQRPayload(ep Endpoint, code string) string {
	q := "name=" + urlQueryEscape(ep.Name) + "&public_key=" + ep.PublicKey + "&type=" + strconv.Itoa(ep.Type) + "&message=" + urlQueryEscape(code)
	return "meshcore://contact/add?" + q
}

func urlQueryEscape(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, " ", "+"), "\n", "")
}

func qrDataURL(payload string) string {
	png, err := qrcode.Encode(payload, qrcode.Medium, 280)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func isAckType(t string) bool {
	switch t {
	case "ACK", "PATH", "PATH_IDENTITY", "RESPONSE", "MULTIPART":
		return true
	default:
		return false
	}
}

func observationKey(o Observation) string {
	path, _ := json.Marshal(o.Path)
	return o.PacketHash + "|" + o.Direction + "|" + o.Source + "|" + strVal(o.ObserverID) + "|" + string(path)
}

func deriveStatus(t *Test) string {
	if t.ReplyEndpointAckAt != nil {
		return "completed"
	}
	if tm, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && !tm.After(time.Now()) {
		return "expired"
	}
	if t.Status == "failed" {
		return "failed"
	}
	if t.ReplyBroadcastAt != nil || t.ReturnSeenAt != nil || t.ReplyAckSeenAt != nil {
		return "replying"
	}
	if t.OutboundSeenAt != nil || t.OutboundEndpointSeenAt != nil {
		return "detected"
	}
	return "waiting"
}

func activeFacts(t *Test) map[string]*string {
	return map[string]*string{
		"outbound_seen_at": t.OutboundSeenAt, "outbound_endpoint_seen_at": t.OutboundEndpointSeenAt, "outbound_ack_seen_at": t.OutboundAckSeenAt,
		"reply_broadcast_at": t.ReplyBroadcastAt, "return_seen_at": t.ReturnSeenAt, "reply_ack_seen_at": t.ReplyAckSeenAt, "reply_endpoint_ack_at": t.ReplyEndpointAckAt,
		"outbound_hash": t.OutboundHash, "outbound_ack_hash": t.OutboundAckHash, "outbound_ack_crc_hex": t.OutboundAckCRCHex,
		"return_hash": t.ReturnHash, "reply_hash": t.ReplyHash, "reply_ack_hash": t.ReplyAckHash, "reply_ack_crc_hex": t.ReplyAckCRCHex,
		"outbound_hex": t.OutboundHex, "outbound_ack_hex": t.OutboundAckHex, "return_hex": t.ReturnHex, "reply_hex": t.ReplyHex, "reply_ack_hex": t.ReplyAckHex, "reply_status": t.ReplyStatus,
		"updated_at": &t.UpdatedAt, "status": &t.Status,
	}
}

func stringField(record map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := record[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parsePath(values ...any) []string {
	for _, v := range values {
		switch vv := v.(type) {
		case []any:
			out := make([]string, 0, len(vv))
			for _, item := range vv {
				out = append(out, fmt.Sprint(item))
			}
			return out
		case string:
			var arr []string
			if json.Unmarshal([]byte(vv), &arr) == nil {
				return arr
			}
		}
	}
	return nil
}

func parseStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func normalizeRows(payload any) []any {
	if arr, ok := payload.([]any); ok {
		return arr
	}
	if m, ok := payload.(map[string]any); ok {
		for _, key := range []string{"data", "nodes", "observers"} {
			if arr, ok := m[key].([]any); ok {
				return arr
			}
		}
	}
	return nil
}

func normalizeNodes(payload any, source string) []NodeRecord {
	rows := normalizeRows(payload)
	now := time.Now().UTC().Format(time.RFC3339)
	out := []NodeRecord{}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		pub := stringField(m, "public_key", "publicKey", "pub_key", "id", "node_id")
		if pub == "" {
			continue
		}
		name := firstNonEmpty(stringField(m, "name", "node_name", "observer_name"), "Node "+pub[:min(8, len(pub))])
		short := firstNonEmpty(stringField(m, "short_hash", "hash", "destHash"), pub[:min(2, len(pub))])
		out = append(out, NodeRecord{PublicKey: pub, Name: name, ShortHash: short, Lat: numberPtr(m, "lat", "latitude"), Lon: numberPtr(m, "lon", "lng", "longitude"), UpdatedAt: firstNonEmpty(stringField(m, "updated_at", "last_seen", "timestamp"), now), Source: source})
	}
	return out
}

func normalizeObservers(payload any, source string) []ObserverRecord {
	rows := normalizeRows(payload)
	now := time.Now().UTC().Format(time.RFC3339)
	out := []ObserverRecord{}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		id := stringField(m, "id", "public_key", "publicKey", "observer_id", "observerId")
		if id == "" {
			continue
		}
		out = append(out, ObserverRecord{ID: id, Name: firstNonEmpty(stringField(m, "name", "observer_name", "observerName"), "Observer "+id[:min(8, len(id))]), LastSeen: ptrIf(stringField(m, "last_seen", "lastSeen", "last_packet_at", "lastPacketAt")), Lat: numberPtr(m, "lat", "latitude"), Lon: numberPtr(m, "lon", "lng", "longitude"), UpdatedAt: now, Source: source})
	}
	return out
}

func numberPtr(m map[string]any, keys ...string) *float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			if !math.IsNaN(v) {
				return &v
			}
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
	}
	return nil
}

func (s *Store) scanTest(row scanner, eps []Endpoint, withObs bool) (*Test, error) {
	var t Test
	var outboundSeen, outboundEndpoint, outboundAck, replyBroadcast, returnSeen, replyAck, replyEndpoint sql.NullString
	var outboundHash, outboundAckHash, outboundAckCRC, returnHash, replyHash, replyAckHash, replyAckCRC sql.NullString
	var outboundHex, outboundAckHex, returnHex, replyHex, replyAckHex, replyStatus sql.NullString
	err := row.Scan(&t.ID, &t.BrowserID, &t.UserPublicKey, &t.EndpointID, &t.EndpointName, &t.EndpointRegion, &t.EndpointPublicKey, &t.Code, &t.Status, &t.QRPayload, &outboundSeen, &outboundEndpoint, &outboundAck, &replyBroadcast, &returnSeen, &replyAck, &replyEndpoint, &outboundHash, &outboundAckHash, &outboundAckCRC, &returnHash, &replyHash, &replyAckHash, &replyAckCRC, &outboundHex, &outboundAckHex, &returnHex, &replyHex, &replyAckHex, &replyStatus, &t.CreatedAt, &t.UpdatedAt, &t.ExpiresAt)
	if err != nil {
		return nil, err
	}
	assign := func(ns sql.NullString) *string {
		if ns.Valid {
			return &ns.String
		}
		return nil
	}
	t.OutboundSeenAt, t.OutboundEndpointSeenAt, t.OutboundAckSeenAt, t.ReplyBroadcastAt, t.ReturnSeenAt, t.ReplyAckSeenAt, t.ReplyEndpointAckAt = assign(outboundSeen), assign(outboundEndpoint), assign(outboundAck), assign(replyBroadcast), assign(returnSeen), assign(replyAck), assign(replyEndpoint)
	t.OutboundHash, t.OutboundAckHash, t.OutboundAckCRCHex, t.ReturnHash, t.ReplyHash, t.ReplyAckHash, t.ReplyAckCRCHex = assign(outboundHash), assign(outboundAckHash), assign(outboundAckCRC), assign(returnHash), assign(replyHash), assign(replyAckHash), assign(replyAckCRC)
	t.OutboundHex, t.OutboundAckHex, t.ReturnHex, t.ReplyHex, t.ReplyAckHex, t.ReplyStatus = assign(outboundHex), assign(outboundAckHex), assign(returnHex), assign(replyHex), assign(replyAckHex), assign(replyStatus)
	t.Nodes = map[string]NodeRef{}
	t.Observations = []Observation{}
	for i := range eps {
		if eps[i].ID == t.EndpointID {
			t.EndpointLocation = eps[i].Location
			break
		}
	}
	if withObs {
		obs, err := s.ListObservations(t.ID)
		if err != nil {
			return nil, err
		}
		t.Observations = obs
	}
	t.Status = deriveStatus(&t)
	return &t, nil
}

type scanner interface{ Scan(dest ...any) error }

const testColumns = `id,browser_id,user_public_key,endpoint_id,endpoint_name,endpoint_region,endpoint_public_key,code,status,qr_payload,outbound_seen_at,outbound_endpoint_seen_at,outbound_ack_seen_at,reply_broadcast_at,return_seen_at,reply_ack_seen_at,reply_endpoint_ack_at,outbound_hash,outbound_ack_hash,outbound_ack_crc_hex,return_hash,reply_hash,reply_ack_hash,reply_ack_crc_hex,outbound_hex,outbound_ack_hex,return_hex,reply_hex,reply_ack_hex,reply_status,created_at,updated_at,expires_at`

func (s *Store) GetTest(id string, eps []Endpoint) (*Test, error) {
	row := s.db.QueryRow(`select `+testColumns+` from tests where id=?`, id)
	t, err := s.scanTest(row, eps, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *Store) ListActiveTests(eps []Endpoint) ([]Test, error) {
	rows, err := s.db.Query(`select `+testColumns+` from tests where expires_at > ? order by created_at asc`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Test{}
	for rows.Next() {
		t, err := s.scanTest(rows, eps, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTest(t *Test) error {
	_, err := s.db.Exec(`insert into tests (id,browser_id,user_public_key,endpoint_id,endpoint_name,endpoint_region,endpoint_public_key,code,status,qr_payload,created_at,updated_at,expires_at) values (?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.BrowserID, t.UserPublicKey, t.EndpointID, t.EndpointName, t.EndpointRegion, t.EndpointPublicKey, t.Code, t.Status, t.QRPayload, t.CreatedAt, t.UpdatedAt, t.ExpiresAt)
	return err
}

func (s *Store) TestExists(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`select count(*) from tests where id=?`, id).Scan(&n)
	return n > 0, err
}

func (s *Store) ListTestsForBrowser(browserID string, limit, offset int, eps []Endpoint) ([]Test, error) {
	rows, err := s.db.Query(`select `+testColumns+` from tests where browser_id=? order by created_at desc limit ? offset ?`, browserID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Test{}
	for rows.Next() {
		t, err := s.scanTest(rows, eps, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CountTestsForBrowser(browserID string) (int, error) {
	var n int
	err := s.db.QueryRow(`select count(*) from tests where browser_id=?`, browserID).Scan(&n)
	return n, err
}

func (s *Store) GetTestMetas(ids []string, eps []Endpoint) ([]Test, error) {
	if len(ids) == 0 {
		return []Test{}, nil
	}
	qs := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`select `+testColumns+`, (select count(*) from observations o where o.test_id=tests.id) from tests where id in (`+qs+`) order by created_at desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Test{}
	for rows.Next() {
		var count int
		w := &metaScanner{Rows: rows, Count: &count}
		t, err := s.scanTest(w, eps, false)
		if err != nil {
			return nil, err
		}
		t.ObservationCount = &count
		out = append(out, *t)
	}
	return out, rows.Err()
}

type metaScanner struct {
	*sql.Rows
	Count *int
}

func (m *metaScanner) Scan(dest ...any) error { return m.Rows.Scan(append(dest, m.Count)...) }

func (s *Store) ListObservations(testID string) ([]Observation, error) {
	rows, err := s.db.Query(`select id,direction,source,packet_hash,observer_id,observer_name,hop_count,path_json,path_keys_json,decoded_type,created_at from observations where test_id=? order by created_at desc`, testID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	for rows.Next() {
		var o Observation
		var oid, oname, dtype sql.NullString
		var pathJSON, pathKeysJSON string
		if err := rows.Scan(&o.ID, &o.Direction, &o.Source, &o.PacketHash, &oid, &oname, &o.HopCount, &pathJSON, &pathKeysJSON, &dtype, &o.CreatedAt); err != nil {
			return nil, err
		}
		if oid.Valid {
			o.ObserverID = &oid.String
		}
		if oname.Valid {
			o.ObserverName = &oname.String
		}
		if dtype.Valid {
			o.DecodedType = &dtype.String
		}
		_ = json.Unmarshal([]byte(pathJSON), &o.Path)
		_ = json.Unmarshal([]byte(pathKeysJSON), &o.PathKeys)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) AddObservation(testID string, o Observation) error {
	pathJSON, _ := json.Marshal(o.Path)
	pathKeysJSON, _ := json.Marshal(o.PathKeys)
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists int
	_ = s.db.QueryRow(`select count(*) from observations where test_id=? and packet_hash=? and direction=? and source=? and coalesce(observer_id,'')=coalesce(?,'') and path_json=?`, testID, o.PacketHash, o.Direction, o.Source, o.ObserverID, string(pathJSON)).Scan(&exists)
	if exists > 0 {
		return nil
	}
	_, err := s.db.Exec(`insert into observations (test_id,direction,source,packet_hash,observer_id,observer_name,hop_count,path_json,path_keys_json,decoded_type,created_at) values (?,?,?,?,?,?,?,?,?,?,?)`, testID, o.Direction, o.Source, o.PacketHash, o.ObserverID, o.ObserverName, o.HopCount, string(pathJSON), string(pathKeysJSON), o.DecodedType, o.CreatedAt)
	return err
}

func (s *Store) UpdateFacts(testID string, f map[string]*string) error {
	_, err := s.db.Exec(`update tests set outbound_seen_at=coalesce(?,outbound_seen_at),outbound_endpoint_seen_at=coalesce(?,outbound_endpoint_seen_at),outbound_ack_seen_at=coalesce(?,outbound_ack_seen_at),reply_broadcast_at=coalesce(?,reply_broadcast_at),return_seen_at=coalesce(?,return_seen_at),reply_ack_seen_at=coalesce(?,reply_ack_seen_at),reply_endpoint_ack_at=coalesce(?,reply_endpoint_ack_at),outbound_hash=coalesce(?,outbound_hash),outbound_ack_hash=coalesce(?,outbound_ack_hash),outbound_ack_crc_hex=coalesce(?,outbound_ack_crc_hex),return_hash=coalesce(?,return_hash),reply_hash=coalesce(?,reply_hash),reply_ack_hash=coalesce(?,reply_ack_hash),reply_ack_crc_hex=coalesce(?,reply_ack_crc_hex),outbound_hex=coalesce(?,outbound_hex),outbound_ack_hex=coalesce(?,outbound_ack_hex),return_hex=coalesce(?,return_hex),reply_hex=coalesce(?,reply_hex),reply_ack_hex=coalesce(?,reply_ack_hex),reply_status=coalesce(?,reply_status),updated_at=coalesce(?,updated_at),status=coalesce(?,status) where id=?`,
		f["outbound_seen_at"], f["outbound_endpoint_seen_at"], f["outbound_ack_seen_at"], f["reply_broadcast_at"], f["return_seen_at"], f["reply_ack_seen_at"], f["reply_endpoint_ack_at"], f["outbound_hash"], f["outbound_ack_hash"], f["outbound_ack_crc_hex"], f["return_hash"], f["reply_hash"], f["reply_ack_hash"], f["reply_ack_crc_hex"], f["outbound_hex"], f["outbound_ack_hex"], f["return_hex"], f["reply_hex"], f["reply_ack_hex"], f["reply_status"], f["updated_at"], f["status"], testID)
	return err
}

func (s *Store) UpsertNodes(nodes []NodeRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`insert into nodes (public_key,name,short_hash,node_type,lat,lon,updated_at,source) values (?,?,?,?,?,?,?,?) on conflict(public_key) do update set name=excluded.name,short_hash=excluded.short_hash,node_type=excluded.node_type,lat=excluded.lat,lon=excluded.lon,updated_at=excluded.updated_at,source=excluded.source`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, n := range nodes {
		_, _ = stmt.Exec(n.PublicKey, n.Name, n.ShortHash, n.NodeType, n.Lat, n.Lon, n.UpdatedAt, n.Source)
	}
	return tx.Commit()
}

func (s *Store) ListNodes(q string, limit int, updatedAfter *string) ([]NodeRecord, error) {
	search := "%" + strings.ToLower(q) + "%"
	args := []any{search, search, search}
	sqlText := `select public_key,name,short_hash,node_type,lat,lon,updated_at,source from nodes where (lower(name) like ? or lower(public_key) like ? or lower(short_hash) like ?)`
	if updatedAfter != nil {
		sqlText += ` and updated_at >= ?`
		args = append(args, *updatedAfter)
	}
	sqlText += ` order by updated_at desc, name asc limit ?`
	args = append(args, limit)
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeRecord{}
	for rows.Next() {
		var n NodeRecord
		var typ sql.NullInt64
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&n.PublicKey, &n.Name, &n.ShortHash, &typ, &lat, &lon, &n.UpdatedAt, &n.Source); err != nil {
			return nil, err
		}
		if typ.Valid {
			v := int(typ.Int64)
			n.NodeType = &v
		}
		if lat.Valid {
			n.Lat = &lat.Float64
		}
		if lon.Valid {
			n.Lon = &lon.Float64
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CountNodes() (int, error) {
	var n int
	err := s.db.QueryRow(`select count(*) from nodes`).Scan(&n)
	return n, err
}

func sortTests(tests []Test) {
	sort.Slice(tests, func(i, j int) bool { return tests[i].CreatedAt > tests[j].CreatedAt })
}
