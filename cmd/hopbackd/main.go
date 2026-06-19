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
	"io"
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
	packetQueueSize = 65536 // inbound packets awaiting decode/match
	dbQueueSize     = 65536 // observation/fact writes awaiting persistence
	clientQueueSize = 4096  // per-browser outbound websocket messages
	tsMillis        = "2006-01-02T15:04:05.000Z07:00"
)

//go:embed all:frontend
var frontendFiles embed.FS

// cloneFacts copies a fact map into independent values so an async DB write can
// hold it without aliasing a Test struct that the publish path may still mutate.
func cloneFacts(f map[string]*string) map[string]*string {
	out := make(map[string]*string, len(f))
	for k, v := range f {
		if v != nil {
			s := *v
			out[k] = &s
		}
	}
	return out
}

// publishCopy returns an independent Test struct for the publish path. decorate
// owns the maps/slices it mutates, so a shallow struct copy is enough here to
// keep its field reassignments off the live test.
func publishCopy(t *Test) *Test {
	cp := *t
	return &cp
}

type Config struct {
	Service struct {
		Name           string `yaml:"name"`
		DatabasePath   string `yaml:"databasePath"`
		Verbose        bool   `yaml:"verbose"`
		AutoReply      *bool  `yaml:"autoReply"`
		TestTTLMinutes int    `yaml:"testTtlMinutes"`
		// MonitorWindowMinutes is the minimum time we keep capturing packets for a
		// test after its first packet is seen, regardless of whether it already
		// completed. Ensures late flood retransmissions are fully captured.
		MonitorWindowMinutes int `yaml:"monitorWindowMinutes"`
		// ReplyFloodFallbackSeconds is how long we wait after a direct-routed reply
		// before re-sending it as a flood when no reply-ACK has been observed, so a
		// stale path never loses the message. Zero disables the fallback.
		ReplyFloodFallbackSeconds int    `yaml:"replyFloodFallbackSeconds"`
		PrivateKey                string `yaml:"privateKey"`
		PublicKey                 string `yaml:"publicKey"`
		AgentSecret               string `yaml:"agentSecret"`
		// Network scopes this instance to one MeshCore network. Surfaced on the
		// homepage so users know which network these diagnostics cover; each
		// Hopback deployment serves a single network.
		Network struct {
			Name string `yaml:"name"`
			URL  string `yaml:"url"`
			// Flag is an optional emoji (e.g. "🇨🇿") shown next to the title to make
			// the instance's region obvious at a glance.
			Flag string `yaml:"flag"`
			// Message overrides the homepage scope notice with a locale-keyed string
			// (e.g. message.en / message.cs). Falls back to a templated default.
			Message map[string]string `yaml:"message"`
		} `yaml:"network"`
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
	Network         *NetworkInfo     `json:"network,omitempty"`
}

// NetworkInfo names the single MeshCore network this instance is scoped to.
type NetworkInfo struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	Flag string `json:"flag,omitempty"`
	// Message is an optional locale-keyed override for the homepage scope notice.
	Message map[string]string `json:"message,omitempty"`
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
	ID                     string                          `json:"id"`
	BrowserID              string                          `json:"browserId"`
	UserPublicKey          string                          `json:"userPublicKey"`
	EndpointID             string                          `json:"endpointId"`
	EndpointName           string                          `json:"endpointName"`
	EndpointRegion         string                          `json:"endpointRegion"`
	EndpointPublicKey      string                          `json:"endpointPublicKey"`
	EndpointLocation       *Location                       `json:"endpointLocation,omitempty"`
	Code                   string                          `json:"code"`
	Status                 string                          `json:"status"`
	QRPayload              string                          `json:"qrPayload"`
	QRDataURL              string                          `json:"qrDataUrl,omitempty"`
	OutboundSeenAt         *string                         `json:"outboundSeenAt,omitempty"`
	OutboundEndpointSeenAt *string                         `json:"outboundEndpointSeenAt,omitempty"`
	OutboundAckSeenAt      *string                         `json:"outboundAckSeenAt,omitempty"`
	ReplyBroadcastAt       *string                         `json:"replyBroadcastAt,omitempty"`
	ReturnSeenAt           *string                         `json:"returnSeenAt,omitempty"`
	ReplyAckSeenAt         *string                         `json:"replyAckSeenAt,omitempty"`
	ReplyEndpointAckAt     *string                         `json:"replyEndpointAckAt,omitempty"`
	OutboundHash           *string                         `json:"outboundHash,omitempty"`
	OutboundAckHash        *string                         `json:"outboundAckHash,omitempty"`
	OutboundAckCRCHex      *string                         `json:"outboundAckCrcHex,omitempty"`
	ReturnHash             *string                         `json:"returnHash,omitempty"`
	ReplyHash              *string                         `json:"replyHash,omitempty"`
	ReplyAckHash           *string                         `json:"replyAckHash,omitempty"`
	ReplyAckCRCHex         *string                         `json:"replyAckCrcHex,omitempty"`
	OutboundHex            *string                         `json:"outboundHex,omitempty"`
	OutboundAckHex         *string                         `json:"outboundAckHex,omitempty"`
	ReturnHex              *string                         `json:"returnHex,omitempty"`
	ReplyHex               *string                         `json:"replyHex,omitempty"`
	ReplyAckHex            *string                         `json:"replyAckHex,omitempty"`
	ReplyStatus            *string                         `json:"replyStatus,omitempty"`
	CreatedAt              string                          `json:"createdAt"`
	UpdatedAt              string                          `json:"updatedAt"`
	ExpiresAt              string                          `json:"expiresAt"`
	Observations           []Observation                   `json:"observations"`
	Nodes                  map[string]NodeRef              `json:"nodes"`
	ObservationCount       *int                            `json:"observationCount,omitempty"`
	DeliveryPaths          map[string][]DeliveryPathOption `json:"deliveryPaths,omitempty"`
	PropagationMap         *PropagationMapData             `json:"propagationMap,omitempty"`
	PathStatistics         *PathStatistics                 `json:"pathStatistics,omitempty"`
	// hopCandidates holds the rival nodes for each colliding short path hash
	// (keyed by lowercased hex). Transient: rebuilt by decorate, never persisted.
	hopCandidates map[string][]NodeRef `json:"-"`
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
	DistanceKm   *float64  `json:"distanceKm,omitempty"`
	DecodedType  *string   `json:"decodedType,omitempty"`
	// Route is the packet header's routing method ("direct" or "flood"), i.e. how
	// the packet was actually sent — not inferred from hop counts.
	Route     *string `json:"route,omitempty"`
	CreatedAt string  `json:"createdAt"`
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

type DeliveryPathRow struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Meta      string   `json:"meta"`
	Short     string   `json:"short"`
	PublicKey *string  `json:"publicKey,omitempty"`
	ShortHash *string  `json:"shortHash,omitempty"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	HasCoords bool     `json:"hasCoords,omitempty"`
	// Hex is the raw per-hop path hash as seen on the mesh; its width (1b/2b/3b…)
	// reflects how the packet's routing encoded the path.
	Hex string `json:"hex,omitempty"`
	// Conflict is set when the hop's short path hash collided with several known
	// nodes; Alternatives lists the runners-up (the chosen node is this row).
	Conflict     bool      `json:"conflict,omitempty"`
	Alternatives []NodeRef `json:"alternatives,omitempty"`
	Tone         string    `json:"tone"`
}

type DeliveryPathOption struct {
	Key           string            `json:"key"`
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	HopCount  int    `json:"hopCount"`
	// HashWidth is the per-hop path-hash size in bytes (1/2/3) the packet's routing
	// used to address relays. 0 when there are no hops (a direct packet).
	HashWidth     int               `json:"hashWidth,omitempty"`
	ObservationID int64             `json:"observationId"`
	PacketHash    string            `json:"packetHash"`
	CreatedAt     string            `json:"createdAt"`
	Rows          []DeliveryPathRow `json:"rows"`
}

type PropagationMapPoint struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	PublicKey *string `json:"publicKey,omitempty"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
}

type PropagationMapSegment struct {
	Key       string     `json:"key"`
	Direction string     `json:"direction"`
	Kind      string     `json:"kind"`
	From      [2]float64 `json:"from"`
	To        [2]float64 `json:"to"`
}

type PropagationMapData struct {
	Points   []PropagationMapPoint   `json:"points"`
	Segments []PropagationMapSegment `json:"segments"`
}

type PathStatistics struct {
	TotalPaths           int      `json:"totalPaths"`
	OutboundPaths        int      `json:"outboundPaths"`
	ReturnPaths          int      `json:"returnPaths"`
	LongestDistanceKm    *float64 `json:"longestDistanceKm,omitempty"`
	LongestDistanceLabel *string  `json:"longestDistanceLabel,omitempty"`
	LongestHopCount      int      `json:"longestHopCount"`
	ShortestHopCount     int      `json:"shortestHopCount"`
	AverageDistanceKm    *float64 `json:"averageDistanceKm,omitempty"`
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
	dbCh      chan func(dbExec) error
	mu        sync.RWMutex
}

// dbExec is satisfied by both *sql.DB and *sql.Tx, letting a write run either
// standalone or as part of a batched transaction.
type dbExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type ActiveTest struct {
	Test *Test
	Keys map[string]bool
	// FirstPacketAt is the RFC3339 (UTC) time the first packet for this test was
	// observed. Empty until the first packet arrives. Anchors the monitor window.
	FirstPacketAt string
	// ReplyFallbackSent guards the one-shot flood re-send of a direct reply that
	// went unacknowledged.
	ReplyFallbackSent bool
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
	ReceivedAt   time.Time
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
	subsMu    sync.RWMutex
	testIDs   map[string]bool
	out       chan any
	closed    chan struct{}
	closeOnce sync.Once
}

// send queues a message for the client's write pump. It never blocks the caller:
// if a slow browser can't keep up, the oldest queued messages are dropped (the
// client re-fetches full state on its next reload). This keeps one slow socket
// from stalling the whole packet pipeline.
func (c *BrowserClient) send(v any) {
	for {
		select {
		case c.out <- v:
			return
		case <-c.closed:
			return
		default:
			select { // make room by dropping the oldest queued message
			case <-c.out:
			default:
			}
		}
	}
}

// writePump owns all writes to the websocket. Gorilla allows only one concurrent
// writer, so funnelling every write through this single goroutine is what makes
// concurrent send() calls safe.
func (c *BrowserClient) writePump() {
	for {
		select {
		case v := <-c.out:
			if err := c.conn.WriteJSON(v); err != nil {
				return
			}
		case <-c.closed:
			return
		}
	}
}

func (c *BrowserClient) close() { c.closeOnce.Do(func() { close(c.closed) }) }

func (c *BrowserClient) subscribe(testID string) {
	c.subsMu.Lock()
	c.testIDs[testID] = true
	c.subsMu.Unlock()
}

func (c *BrowserClient) subscribed(testID string) bool {
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	return c.testIDs[testID]
}

type AgentClient struct {
	conn        *websocket.Conn
	ID          string
	EndpointID  string
	IPCReady    bool
	ConnectedAt string
	LastSeenAt  string
	writeMu     sync.Mutex
}

func (a *AgentClient) send(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteJSON(v)
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
	if cfg.Service.MonitorWindowMinutes == 0 {
		cfg.Service.MonitorWindowMinutes = 15
	}
	if cfg.Service.ReplyFloodFallbackSeconds == 0 {
		cfg.Service.ReplyFloodFallbackSeconds = 8
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
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on")
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
		route text,
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
	if err != nil {
		return err
	}
	// Best-effort column add for databases created before `route` existed; the
	// error when it already exists is expected and ignored.
	_, _ = s.db.Exec(`alter table observations add column route text`)
	return nil
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
		dbCh:      make(chan func(dbExec) error, dbQueueSize),
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
	// One ordered packet worker (decode + match only — the heavy work is offloaded)
	// and one DB writer keep the pipeline simple and deterministic.
	go rt.packetWorker()
	go rt.dbWriter()
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
		rt.mu.Unlock()
	}
}

// dbWriter persists observations and fact updates off the packet worker's hot
// path. Writes are batched into a single transaction per drain so a burst of
// dozens of packets costs one commit instead of dozens.
func (rt *Runtime) dbWriter() {
	for job := range rt.dbCh {
		batch := []func(dbExec) error{job}
		for len(batch) < 512 {
			select {
			case j := <-rt.dbCh:
				batch = append(batch, j)
			default:
				goto run
			}
		}
	run:
		rt.store.runBatch(batch)
	}
}

// enqueueWrite hands a DB write to the async writer without blocking. If the
// queue is somehow saturated it falls back to a synchronous write rather than
// losing the record.
func (rt *Runtime) enqueueWrite(job func(dbExec) error) {
	select {
	case rt.dbCh <- job:
	default:
		rt.store.runBatch([]func(dbExec) error{job})
	}
}

func (rt *Runtime) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", rt.handleStatus)
	mux.HandleFunc("GET /api/nodes", rt.handleNodes)
	mux.HandleFunc("GET /api/tests", rt.handleTestsList)
	mux.HandleFunc("POST /api/tests", rt.handleCreateTest)
	mux.HandleFunc("POST /api/tests/meta", rt.handleTestMetas)
	mux.HandleFunc("GET /api/tests/{id}", rt.handleGetTest)
	mux.HandleFunc("GET /api/analyzer/packet/{hash}", rt.handleAnalyzerPacket)
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
	var network *NetworkInfo
	if name := strings.TrimSpace(rt.cfg.Service.Network.Name); name != "" {
		network = &NetworkInfo{Name: name, URL: strings.TrimSpace(rt.cfg.Service.Network.URL), Flag: strings.TrimSpace(rt.cfg.Service.Network.Flag), Message: rt.cfg.Service.Network.Message}
	}
	return RuntimeStatus{Analyzers: analyzers, Endpoints: eps, Agents: agents, Nodes: nodes, Observers: len(rt.observers), ActiveObservers: activeObs, ActiveTests: len(rt.active), Verbose: rt.cfg.Service.Verbose, Network: network}
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

// handleAnalyzerPacket proxies the analyzer's packet API so the browser can read
// it: the analyzer serves no CORS headers, so a direct frontend fetch is blocked.
// The frontend uses this to map our observations to the analyzer's observation ids.
func (rt *Runtime) handleAnalyzerPacket(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(strings.TrimSpace(r.PathValue("hash")))
	if !isHex(hash) || len(hash) < 4 || len(hash) > 64 {
		writeJSONStatus(w, 400, map[string]string{"message": "invalid packet hash"})
		return
	}
	if len(rt.cfg.CoreScope.URLs) == 0 {
		writeJSONStatus(w, 404, map[string]string{"message": "no analyzer configured"})
		return
	}
	base := wsToHTTP(rt.cfg.CoreScope.URLs[0])
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/packets/"+hash, nil)
	if err != nil {
		writeJSONStatus(w, 502, map[string]string{"message": "analyzer request failed"})
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSONStatus(w, 502, map[string]string{"message": "analyzer unreachable"})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}

func (rt *Runtime) handleTestsList(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 30, 1, 100)
	offset := intParam(r, "offset", 0, 0, 100000)
	localIDs := idListParam(r, "ids", 200)
	tests, err := rt.store.ListVisibleTestMetas(localIDs, limit, offset, rt.cfg.Endpoints)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	total, _ := rt.store.CountVisibleTests(localIDs)
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
	// decorate mutates Nodes and per-observation fields, so it must own that data.
	// Callers pass shallow copies of the live test, which still alias its Nodes map
	// and Observations backing array; give this call independent copies so two
	// goroutines decorating concurrently never touch the same map/slice.
	t.Nodes = map[string]NodeRef{}
	// make+copy (not append) so an empty observation list stays a non-nil slice
	// and marshals as [] rather than null, which the frontend filters over.
	obs := make([]Observation, len(t.Observations))
	copy(obs, t.Observations)
	t.Observations = obs
	for i := range t.Observations {
		if t.Observations[i].Path == nil {
			t.Observations[i].Path = []string{}
		}
		if t.Observations[i].PathKeys == nil {
			t.Observations[i].PathKeys = []*string{}
		}
	}
	rt.resolveObservationNodes(t)
	t.Status = deriveStatus(t)
	t.DeliveryPaths = rt.deliveryPaths(t)
	t.PropagationMap = rt.propagationMap(t)
	t.PathStatistics = pathStatistics(t)
	return t
}

func (rt *Runtime) resolveObservationNodes(t *Test) {
	t.hopCandidates = map[string][]NodeRef{}
	keys := []string{t.UserPublicKey, t.EndpointPublicKey}
	hashSet := map[string]bool{}
	for _, obs := range t.Observations {
		if obs.ObserverID != nil {
			keys = append(keys, *obs.ObserverID)
		}
		// The full pubkeys CoreScope resolved are exact lookup keys for hops.
		for _, pk := range obs.PathKeys {
			if pk != nil {
				keys = append(keys, *pk)
			}
		}
		// Hops without a full-pubkey hint carry only a short path hash, which
		// collides across many nodes; gather them for candidate resolution.
		for j, hop := range obs.Path {
			hasHint := j < len(obs.PathKeys) && obs.PathKeys[j] != nil && strings.TrimSpace(*obs.PathKeys[j]) != ""
			h := strings.ToLower(strings.TrimSpace(hop))
			if !hasHint && h != "" {
				hashSet[h] = true
			}
		}
	}
	nodes, _ := rt.store.LookupNodes(keys)
	for key, node := range nodes {
		t.Nodes[key] = node
	}
	hashList := make([]string, 0, len(hashSet))
	for h := range hashSet {
		hashList = append(hashList, h)
	}
	candidates, _ := rt.store.LookupNodeCandidates(hashList)
	// Re-rank collisions by geography: a relay within map range of the endpoint
	// is far more plausible than one resolved to bogus, globe-spanning coords.
	endpoint := rt.endpointCoords(t)
	for h, list := range candidates {
		candidates[h] = rankCandidates(list, endpoint)
	}
	for i := range t.Observations {
		obs := &t.Observations[i]
		if obs.ObserverID != nil {
			if key := nodeKey(*obs.ObserverID, nodes); key != "" {
				obs.ObserverKey = &key
			}
		}
		hints := obs.PathKeys
		obs.PathKeys = make([]*string, len(obs.Path))
		for j, hop := range obs.Path {
			// 1. Full pubkey CoreScope already resolved for this hop.
			if j < len(hints) && hints[j] != nil && strings.TrimSpace(*hints[j]) != "" {
				hint := strings.ToLower(strings.TrimSpace(*hints[j]))
				key := nodeKey(hint, nodes)
				if key == "" {
					// CoreScope knows the pubkey even though it's not in our node DB:
					// register a minimal node so the name/link still resolve.
					key = hint
					if _, ok := t.Nodes[key]; !ok {
						t.Nodes[key] = NodeRef{PublicKey: hint, Name: "Node " + shortLabel(hint), ShortHash: hop}
					}
				}
				obs.PathKeys[j] = &key
				continue
			}
			// 2. Resolve the short path hash to its most-probable node, keeping the
			// rivals so the UI can flag the collision.
			h := strings.ToLower(strings.TrimSpace(hop))
			cands := candidates[h]
			if len(cands) == 0 {
				continue
			}
			best := cands[0]
			key := strings.ToLower(firstNonEmpty(best.PublicKey, h))
			t.Nodes[key] = best
			obs.PathKeys[j] = &key
			if len(cands) > 1 {
				t.hopCandidates[h] = cands
			}
		}
		if d := rt.observationDistanceKm(t, *obs); d != nil {
			obs.DistanceKm = d
		}
	}
}

// rankCandidates orders collision candidates for a path hash best-first: nodes
// within map range of the endpoint are the most plausible relays, then nodes of
// unknown location, then implausibly distant ones. Within a tier the incoming
// (recency) order is preserved.
func rankCandidates(cands []NodeRef, endpoint *[2]float64) []NodeRef {
	if len(cands) <= 1 {
		return cands
	}
	out := make([]NodeRef, len(cands))
	copy(out, cands)
	tier := func(n NodeRef) int {
		if !hasCoords(n.Lat, n.Lon) {
			return 1
		}
		if endpoint == nil || haversineKm(endpoint[0], endpoint[1], *n.Lat, *n.Lon) <= maxMapDistanceKm {
			return 0
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return tier(out[i]) < tier(out[j]) })
	return out
}

// observationDistanceKm sums the geographic length of the delivery route. Hops
// without coordinates — or resolved to implausibly distant ones (a short-hash
// collision pointing across the globe) — break the chain: the lines into and
// out of such a hop are not counted, rather than bridged, so a single bogus
// coordinate can't inflate the distance to thousands of kilometres.
func (rt *Runtime) observationDistanceKm(t *Test, obs Observation) *float64 {
	endpoint := rt.endpointCoords(t)
	total := 0.0
	counted := false
	var prev *[2]float64
	for _, row := range rt.deliveryRows(t, obs) {
		if !hasCoords(row.Lat, row.Lon) ||
			(endpoint != nil && haversineKm(endpoint[0], endpoint[1], *row.Lat, *row.Lon) > maxMapDistanceKm) {
			prev = nil
			continue
		}
		cur := [2]float64{*row.Lat, *row.Lon}
		if prev != nil {
			total += haversineKm(prev[0], prev[1], cur[0], cur[1])
			counted = true
		}
		prev = &cur
	}
	if !counted {
		return nil
	}
	return &total
}

func (rt *Runtime) isEndpointObs(t *Test, obs Observation) bool {
	if strings.HasPrefix(obs.Source, "agent:") {
		return true
	}
	return obs.ObserverID != nil && strings.EqualFold(*obs.ObserverID, t.EndpointPublicKey)
}

// routeSignature is a stable identity for a delivery route so duplicate paths
// (the same relay sequence seen by different observers) collapse to one entry.
func routeSignature(rows []DeliveryPathRow) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, firstNonEmpty(strVal(r.PublicKey), strVal(r.ShortHash), r.Short, r.Name))
	}
	return strings.Join(parts, ">")
}

// pathHashWidth returns the per-hop path-hash size in bytes, inferred from the
// hex width of the first relay hash (each hop is a fixed-size hash). 0 if empty.
func pathHashWidth(path []string) int {
	for _, h := range path {
		h = strings.TrimSpace(h)
		if h != "" {
			return len(h) / 2
		}
	}
	return 0
}

func (rt *Runtime) deliveryPaths(t *Test) map[string][]DeliveryPathOption {
	paths := map[string][]DeliveryPathOption{"outbound": {}, "return": {}}
	for _, direction := range []string{"outbound", "return"} {
		// A delivery route is only trustworthy when the packet was actually seen at
		// the endpoint (the agent records it) — those are the real routes. Fall back
		// to every observed vantage path only when the endpoint logged nothing.
		var atEndpoint, elsewhere []Observation
		for _, obs := range t.Observations {
			if obs.Direction != direction {
				continue
			}
			if rt.isEndpointObs(t, obs) {
				atEndpoint = append(atEndpoint, obs)
			} else {
				elsewhere = append(elsewhere, obs)
			}
		}
		candidates := atEndpoint
		if len(candidates) == 0 {
			candidates = elsewhere
		}
		seenRoute := map[string]bool{}
		for _, obs := range candidates {
			rows := rt.deliveryRows(t, obs)
			sig := packetKind(obs) + "|" + routeSignature(rows)
			if seenRoute[sig] {
				continue
			}
			seenRoute[sig] = true
			paths[obs.Direction] = append(paths[obs.Direction], DeliveryPathOption{
				Key:           fmt.Sprintf("%s:%d:%s", obs.Direction, obs.ID, obs.PacketHash),
				Direction:     obs.Direction,
				Kind:          packetKind(obs),
				HopCount:      obs.HopCount,
				HashWidth:     pathHashWidth(obs.Path),
				ObservationID: obs.ID,
				PacketHash:    obs.PacketHash,
				CreatedAt:     obs.CreatedAt,
				Rows:          rows,
			})
		}
	}
	for direction := range paths {
		sort.Slice(paths[direction], func(i, j int) bool {
			if paths[direction][i].HopCount == paths[direction][j].HopCount {
				return paths[direction][i].CreatedAt < paths[direction][j].CreatedAt
			}
			return paths[direction][i].HopCount < paths[direction][j].HopCount
		})
	}
	return paths
}

func (rt *Runtime) deliveryRows(t *Test, obs Observation) []DeliveryPathRow {
	rows := []DeliveryPathRow{rt.edgeRow(t, obs.Direction, "start")}
	for i, hop := range obs.Path {
		key := hop
		if i < len(obs.PathKeys) && obs.PathKeys[i] != nil {
			key = *obs.PathKeys[i]
		}
		node := t.Nodes[key]
		name := firstNonEmpty(node.Name, "Node "+shortLabel(hop))
		short := firstNonEmpty(node.ShortHash, hop)
		pub := ptrIf(node.PublicKey)
		shortPtr := ptrIf(short)
		row := DeliveryPathRow{
			Key:       fmt.Sprintf("%s:hop:%d:%s", obs.Direction, i, key),
			Name:      name,
			Meta:      "Hop " + strconv.Itoa(i+1),
			Short:     shortLabel(short),
			PublicKey: pub,
			ShortHash: shortPtr,
			Lat:       node.Lat,
			Lon:       node.Lon,
			HasCoords: hasCoords(node.Lat, node.Lon),
			Hex:       strings.ToUpper(hop),
			Tone:      "hop",
		}
		// Flag a collision when the short path hash matched several nodes; the
		// chosen one is this row, so the rest are the tooltip alternatives.
		if alts := t.hopCandidates[strings.ToLower(strings.TrimSpace(hop))]; len(alts) > 1 {
			row.Conflict = true
			others := alts[1:]
			if len(others) > 8 {
				others = others[:8]
			}
			row.Alternatives = others
		}
		rows = append(rows, row)
	}
	rows = append(rows, rt.edgeRow(t, obs.Direction, "end"))
	return rows
}

func (rt *Runtime) edgeRow(t *Test, direction, edge string) DeliveryPathRow {
	key := direction + ":" + edge
	isUser := (direction == "outbound" && edge == "start") || (direction == "return" && edge == "end")
	if isUser {
		node := t.Nodes[strings.ToLower(t.UserPublicKey)]
		return DeliveryPathRow{
			Key:       key,
			Name:      firstNonEmpty(node.Name, "User app"),
			Meta:      "User public key",
			Short:     shortLabel(firstNonEmpty(node.ShortHash, t.UserPublicKey)),
			PublicKey: &t.UserPublicKey,
			ShortHash: ptrIf(node.ShortHash),
			Lat:       node.Lat,
			Lon:       node.Lon,
			HasCoords: hasCoords(node.Lat, node.Lon),
			Tone:      "edge",
		}
	}
	var lat, lon *float64
	if t.EndpointLocation != nil {
		lat, lon = t.EndpointLocation.Lat, t.EndpointLocation.Lon
	}
	node := t.Nodes[strings.ToLower(t.EndpointPublicKey)]
	if !hasCoords(lat, lon) && hasCoords(node.Lat, node.Lon) {
		lat, lon = node.Lat, node.Lon
	}
	return DeliveryPathRow{
		Key:       key,
		Name:      firstNonEmpty(node.Name, t.EndpointName),
		Meta:      t.EndpointRegion,
		Short:     shortLabel(firstNonEmpty(node.ShortHash, t.EndpointPublicKey)),
		PublicKey: &t.EndpointPublicKey,
		ShortHash: ptrIf(node.ShortHash),
		Lat:       lat,
		Lon:       lon,
		HasCoords: hasCoords(lat, lon),
		Tone:      "edge",
	}
}

// maxMapDistanceKm bounds how far a node can sit from the endpoint and still be
// drawn on the propagation map. Nodes resolved to bogus/distant coordinates would
// otherwise stretch the map across the globe.
const maxMapDistanceKm = 2000

// endpointCoords returns the endpoint's [lat, lon] for distance filtering, or nil.
func (rt *Runtime) endpointCoords(t *Test) *[2]float64 {
	var lat, lon *float64
	if t.EndpointLocation != nil {
		lat, lon = t.EndpointLocation.Lat, t.EndpointLocation.Lon
	}
	if !hasCoords(lat, lon) {
		node := t.Nodes[strings.ToLower(t.EndpointPublicKey)]
		lat, lon = node.Lat, node.Lon
	}
	if !hasCoords(lat, lon) {
		return nil
	}
	return &[2]float64{*lat, *lon}
}

func (rt *Runtime) propagationMap(t *Test) *PropagationMapData {
	out := &PropagationMapData{Points: []PropagationMapPoint{}, Segments: []PropagationMapSegment{}}
	endpoint := rt.endpointCoords(t)
	// tooFar reports whether a coordinate is beyond the map radius from the endpoint.
	tooFar := func(lat, lon float64) bool {
		return endpoint != nil && haversineKm(endpoint[0], endpoint[1], lat, lon) > maxMapDistanceKm
	}
	seen := map[string]bool{}
	addPoint := func(key, name, kind string, pub *string, lat, lon *float64) {
		if !hasCoords(lat, lon) || tooFar(*lat, *lon) || seen[key] {
			return
		}
		seen[key] = true
		out.Points = append(out.Points, PropagationMapPoint{Key: key, Name: name, Kind: kind, PublicKey: pub, Lat: *lat, Lon: *lon})
	}
	for _, obs := range t.Observations {
		// Connect only consecutive relay hops that the packet actually traversed.
		// The endpoint and user are the source/destination, not path relays, so we
		// never synthesize a line to them — that would invent edges that don't exist.
		// A hop without coordinates (or out of range) breaks the chain, no bridging.
		var prevLat, prevLon *float64
		for i, hop := range obs.Path {
			key := hop
			if i < len(obs.PathKeys) && obs.PathKeys[i] != nil {
				key = *obs.PathKeys[i]
			}
			node := t.Nodes[key]
			if !hasCoords(node.Lat, node.Lon) || tooFar(*node.Lat, *node.Lon) {
				prevLat, prevLon = nil, nil
				continue
			}
			addPoint("node:"+firstNonEmpty(node.PublicKey, key), firstNonEmpty(node.Name, hop), "node", ptrIf(node.PublicKey), node.Lat, node.Lon)
			if prevLat != nil {
				out.Segments = append(out.Segments, PropagationMapSegment{
					Key:       fmt.Sprintf("%s:%d:%s", obs.Direction, i, obs.PacketHash),
					Direction: obs.Direction,
					Kind:      packetKind(obs),
					From:      [2]float64{*prevLat, *prevLon},
					To:        [2]float64{*node.Lat, *node.Lon},
				})
			}
			prevLat, prevLon = node.Lat, node.Lon
		}
		if obs.ObserverKey != nil {
			if node := t.Nodes[*obs.ObserverKey]; hasCoords(node.Lat, node.Lon) {
				addPoint("observer:"+*obs.ObserverKey, firstNonEmpty(strVal(obs.ObserverName), node.Name), "observer", ptrIf(node.PublicKey), node.Lat, node.Lon)
			}
		}
	}
	// The endpoint is a standalone marker, not wired into the relay chain.
	if endpoint != nil {
		node := t.Nodes[strings.ToLower(t.EndpointPublicKey)]
		epKey := t.EndpointPublicKey
		addPoint("endpoint:"+epKey, firstNonEmpty(node.Name, t.EndpointName), "endpoint", &epKey, &endpoint[0], &endpoint[1])
	}
	return out
}

func pathStatistics(t *Test) *PathStatistics {
	if len(t.Observations) == 0 {
		return nil
	}
	uniqueAll := map[string]bool{}
	uniqueOut := map[string]bool{}
	uniqueReturn := map[string]bool{}
	longestHop := 0
	maxInt := int(^uint(0) >> 1)
	shortestHop := maxInt
	var longestDistance *float64
	var longestLabel *string
	totalDistance := 0.0
	distanceCount := 0
	for _, obs := range t.Observations {
		key := obs.Direction + ":" + strings.Join(obs.Path, ">")
		uniqueAll[key] = true
		if obs.Direction == "outbound" {
			uniqueOut[key] = true
		}
		if obs.Direction == "return" {
			uniqueReturn[key] = true
		}
		longestHop = max(longestHop, obs.HopCount)
		shortestHop = min(shortestHop, obs.HopCount)
		if obs.DistanceKm != nil {
			totalDistance += *obs.DistanceKm
			distanceCount++
			if longestDistance == nil || *obs.DistanceKm > *longestDistance {
				v := *obs.DistanceKm
				longestDistance = &v
				label := firstNonEmpty(strVal(obs.ObserverName), strVal(obs.ObserverID), obs.Source)
				longestLabel = &label
			}
		}
	}
	if shortestHop == maxInt {
		shortestHop = 0
	}
	var avg *float64
	if distanceCount > 0 {
		v := totalDistance / float64(distanceCount)
		avg = &v
	}
	return &PathStatistics{TotalPaths: len(uniqueAll), OutboundPaths: len(uniqueOut), ReturnPaths: len(uniqueReturn), LongestDistanceKm: longestDistance, LongestDistanceLabel: longestLabel, LongestHopCount: longestHop, ShortestHopCount: shortestHop, AverageDistanceKm: avg}
}


func nodeKey(value string, nodes map[string]NodeRef) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if _, ok := nodes[value]; ok {
		return value
	}
	for key, node := range nodes {
		if strings.EqualFold(node.PublicKey, value) || strings.EqualFold(node.ShortHash, value) {
			return key
		}
	}
	return ""
}

// routeLabel maps a packet header's route type to the way it was sent. Both
// transport variants collapse to their base method.
func routeLabel(r meshpkt.RouteType) string {
	switch r {
	case meshpkt.RouteDirect, meshpkt.RouteTransportDirect:
		return "direct"
	default:
		return "flood"
	}
}

func hasCoords(lat, lon *float64) bool {
	return lat != nil && lon != nil && !math.IsNaN(*lat) && !math.IsNaN(*lon)
}

func shortLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "?"
	}
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func packetKind(obs Observation) string {
	typ := strVal(obs.DecodedType)
	if isAckType(typ) {
		if obs.Direction == "outbound" {
			if typ == "PATH" || typ == "PATH_IDENTITY" {
				return "ack+path"
			}
			return "ack"
		}
		return "reply ack"
	}
	if obs.Direction == "outbound" {
		return "user msg"
	}
	return "reply"
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371.0088
	toRad := func(v float64) float64 { return v * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	rLat1 := toRad(lat1)
	rLat2 := toRad(lat2)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rLat1)*math.Cos(rLat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
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
		// Observations are stored newest-first, so the last one is the oldest:
		// anchor the monitor window to the genuine first packet time.
		if o.CreatedAt != "" {
			active.FirstPacketAt = o.CreatedAt
		}
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
	client := &BrowserClient{
		conn:      conn,
		browserID: r.URL.Query().Get("browserId"),
		testIDs:   map[string]bool{},
		out:       make(chan any, clientQueueSize),
		closed:    make(chan struct{}),
	}
	if client.browserID == "" {
		client.browserID = "anonymous"
	}
	go client.writePump()
	rt.mu.Lock()
	rt.browsers[client] = true
	rt.mu.Unlock()
	client.send(map[string]any{"type": "hello", "status": rt.Status()})
	for {
		var msg struct {
			Type   string `json:"type"`
			TestID string `json:"testId"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == "subscribe" && msg.TestID != "" {
			client.subscribe(msg.TestID)
			if t, _ := rt.getTest(msg.TestID); t != nil {
				client.send(map[string]any{"type": "testUpdated", "test": t})
			}
		}
	}
	rt.mu.Lock()
	delete(rt.browsers, client)
	rt.mu.Unlock()
	client.close()
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
	_ = agent.send(map[string]any{"type": "hello", "id": id, "status": rt.Status()})
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
	if packet.ReceivedAt.IsZero() {
		packet.ReceivedAt = time.Now()
	}
	rt.mu.RLock()
	active := len(rt.active)
	rt.mu.RUnlock()
	if active == 0 {
		return
	}
	select {
	case rt.packetCh <- packet:
	default:
		log.Printf("[runtime] packet queue full, dropping packet from %s", packet.Source)
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
	if event.Hash == "" {
		h := meshpkt.ContentHash(pkt)
		event.Hash = hex.EncodeToString(h[:])
	}
	// No content-hash gate here: every observer's copy of a flooded packet is a
	// distinct observation we want to keep. Genuine duplicates (same observer,
	// same path) are filtered by the per-observation key in recordPacket.

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
	// Stamp the observation with when the packet was captured, at millisecond
	// precision, so bursts arriving inside the same second keep their order.
	captured := event.ReceivedAt
	if captured.IsZero() {
		captured = time.Now()
	}
	now := captured.UTC().Format(tsMillis)
	path := event.Path
	if len(path) == 0 && pkt != nil {
		for _, hop := range pkt.Hops() {
			path = append(path, hex.EncodeToString(hop))
		}
	}
	// Seed per-hop keys with the full pubkeys CoreScope already resolved for this
	// path (resolved_path runs parallel to path). Agent-observed hops only carry
	// the short prefix, which is left to short-hash matching later.
	pathKeys := make([]*string, len(path))
	for i := range path {
		if i < len(event.ResolvedPath) {
			if pk := strings.ToLower(strings.TrimSpace(event.ResolvedPath[i])); pk != "" && !strings.EqualFold(pk, path[i]) {
				pathKeys[i] = &pk
			}
		}
	}
	var routeKind *string
	if pkt != nil {
		routeKind = ptr(routeLabel(pkt.Route))
	}
	obs := Observation{ID: -int64(len(active.Test.Observations) + 1), Direction: direction, Source: event.Source, PacketHash: event.Hash, ObserverID: event.ObserverID, ObserverName: event.ObserverName, HopCount: len(path), Path: path, PathKeys: pathKeys, DecodedType: ptr(typ), Route: routeKind, CreatedAt: now}
	key := observationKey(obs)
	if active.Keys[key] {
		rt.mu.Unlock()
		return false
	}
	active.Keys[key] = true
	active.Test.Observations = append([]Observation{obs}, active.Test.Observations...)
	rt.applyFactsLocked(active.Test, obs, event.RawHex)
	// Keep capturing for at least the monitor window after the first packet for
	// this test, regardless of whether it already completed. This guarantees we
	// record late flood retransmissions instead of dropping them at the
	// creation-time TTL.
	var extendedExpiry string
	if active.FirstPacketAt == "" {
		active.FirstPacketAt = obs.CreatedAt
		window := time.Duration(rt.cfg.Service.MonitorWindowMinutes) * time.Minute
		deadline := time.Now().UTC().Add(window).Format(time.RFC3339)
		if deadline > active.Test.ExpiresAt {
			active.Test.ExpiresAt = deadline
			extendedExpiry = deadline
		}
	}
	// Observations never change the routing index (it keys on test pubkeys/CRCs),
	// so no rebuild is needed here.
	facts := cloneFacts(activeFacts(active.Test))
	pubCopy := publishCopy(active.Test)
	rt.mu.Unlock()

	rt.enqueueWrite(func(e dbExec) error {
		if err := rt.store.AddObservation(e, testID, obs); err != nil {
			return err
		}
		if err := rt.store.UpdateFacts(e, testID, facts); err != nil {
			return err
		}
		if extendedExpiry != "" {
			return rt.store.UpdateExpiry(e, testID, extendedExpiry)
		}
		return nil
	})
	// Publish the decorated observation (resolved, non-nil path/pathKeys/observerKey)
	// rather than the raw one — the client renders the pushed observation directly,
	// and a nil path would crash its handler. It's the first entry (prepended).
	dt := rt.decorate(pubCopy)
	pushObs := obs
	if len(dt.Observations) > 0 {
		pushObs = dt.Observations[0]
	}
	rt.publishObservation(dt, pushObs)
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
		_ = agent.send(map[string]any{"type": "sendRaw", "testId": t.ID, "packetRole": p.Role, "rawHex": p.Hex})
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
	// The recipient's ACK CRC must be computed over the SAME timestamp and attempt
	// the sender used; otherwise the sender's app won't recognise the ACK. The
	// attempt is the low 2 bits of the message flags (0 for a first send), so we
	// must read it from the decoded outbound packet rather than assuming a value.
	attempt := byte(0)
	if d, err := decodeOutboundText(pkt, ep.PrivateKey, t.UserPublicKey); err == nil {
		ts = uint32(d.Timestamp.Unix())
		text = d.Text
		attempt = d.Attempt
	}
	userPub, _ := hex.DecodeString(t.UserPublicKey)
	crc := meshpkt.TextAckCRC(ts, attempt, text, userPub)
	out := []OutPacket{}
	if pkt.HopCount() > 0 {
		var ack []byte
		if len(ep.PrivateKey) == 128 {
			ack, err = meshpkt.PathTextAckReturnPacketFromExpanded(ep.PrivateKey, ep.PublicKey, t.UserPublicKey, ts, attempt, text, pkt.Path, meshpkt.WithPathHashSize(pkt.PathHashSize))
		} else {
			var seed [32]byte
			b, _ := hex.DecodeString(ep.PrivateKey[:64])
			copy(seed[:], b)
			var peer [32]byte
			copy(peer[:], userPub)
			ack, err = meshpkt.PathTextAckReturnPacketFromIdentity(seed, peer, ts, attempt, text, pkt.Path, meshpkt.WithPathHashSize(pkt.PathHashSize))
		}
		if err == nil {
			out = append(out, rt.outPacket("outboundAck", ack, fmt.Sprintf("%08x", crc)))
		}
	} else {
		ack, err := meshpkt.TextAckPacket(ts, attempt, text, userPub)
		if err == nil {
			out = append(out, rt.outPacket("outboundAck", ack, fmt.Sprintf("%08x", crc)))
		}
	}
	// Send the reply DIRECT (unicast) back along the reversed inbound path when the
	// message took relays to reach us; a direct neighbour (0 hops) has no path to
	// follow, so it floods. A flood fallback (see replyFloodFallback) covers a
	// stale direct path so the message is never lost.
	if rm, err := rt.buildReplyMessage(ep, t, &pkt); err == nil {
		out = append(out, rm)
	}
	return out, nil
}

// reversePath reverses the hop order of a MeshCore path. The path is a flat byte
// slice of fixed-size hop hashes, so it's reversed in hashSize-byte chunks (the
// bytes within each hop keep their order).
func reversePath(path []byte, hashSize int) []byte {
	if hashSize <= 0 || len(path) == 0 || len(path)%hashSize != 0 {
		return path
	}
	n := len(path) / hashSize
	out := make([]byte, len(path))
	for i := 0; i < n; i++ {
		copy(out[i*hashSize:(i+1)*hashSize], path[(n-1-i)*hashSize:(n-i)*hashSize])
	}
	return out
}

// buildReplyMessage builds the endpoint's "received" reply. With a multi-hop
// inbound packet it routes DIRECT along the reversed inbound path (no mesh-wide
// flood); a direct neighbour or a nil packet floods. Pass pkt=nil to force flood.
func (rt *Runtime) buildReplyMessage(ep *Endpoint, t *Test, pkt *meshpkt.Packet) (OutPacket, error) {
	replyText := "Hopback " + t.Code + " received by " + t.EndpointName
	// Pin one timestamp so the packet we send and the ACK CRC we expect back agree
	// to the second; two separate time.Now() calls can straddle a second boundary.
	replyTime := time.Now()
	userPub, _ := hex.DecodeString(t.UserPublicKey)
	expanded := len(ep.PrivateKey) == 128
	var seed, peer [32]byte
	if !expanded {
		b, _ := hex.DecodeString(ep.PrivateKey[:64])
		copy(seed[:], b)
		copy(peer[:], userPub)
	}
	var reply []byte
	var err error
	if pkt != nil && pkt.HopCount() > 0 {
		replyPath := reversePath(pkt.Path, int(pkt.PathHashSize))
		if expanded {
			reply, err = meshpkt.DirectTextPacketFromExpandedViaPath(ep.PrivateKey, ep.PublicKey, t.UserPublicKey, replyText, replyTime, 1, replyPath, meshpkt.WithPathHashSize(pkt.PathHashSize))
		} else {
			reply, err = meshpkt.DirectTextPacketFromIdentityViaPath(seed, peer, replyText, replyTime, 1, replyPath, meshpkt.WithPathHashSize(pkt.PathHashSize))
		}
	} else if expanded {
		reply, err = meshpkt.DirectTextPacketFromExpanded(ep.PrivateKey, ep.PublicKey, t.UserPublicKey, replyText, replyTime, 1)
	} else {
		reply, err = meshpkt.DirectTextPacketFromIdentity(seed, peer, replyText, replyTime, 1)
	}
	if err != nil {
		return OutPacket{}, err
	}
	replyCRC := meshpkt.TextAckCRC(uint32(replyTime.Unix()), 1, replyText, mustHex(ep.PublicKey))
	return rt.outPacket("replyMessage", reply, fmt.Sprintf("%08x", replyCRC)), nil
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
	facts := cloneFacts(activeFacts(active.Test))
	rt.enqueueWrite(func(e dbExec) error { return rt.store.UpdateFacts(e, testID, facts) })
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
	facts := cloneFacts(activeFacts(active.Test))
	testCopy := publishCopy(active.Test)
	rt.mu.Unlock()
	rt.enqueueWrite(func(e dbExec) error { return rt.store.UpdateFacts(e, testID, facts) })
	rt.publishTest(rt.decorate(testCopy))
	if ok && role == "outboundAck" {
		rt.sendReply(testCopy)
	}
	if ok && role == "replyMessage" {
		go rt.replyFloodFallback(testID)
	}
}

// replyFloodFallback re-sends the reply as a flood if a direct-routed reply went
// unacknowledged within the configured window. It's a one-shot guard against a
// stale direct path silently dropping the message.
func (rt *Runtime) replyFloodFallback(testID string) {
	secs := rt.cfg.Service.ReplyFloodFallbackSeconds
	if secs <= 0 {
		return
	}
	time.Sleep(time.Duration(secs) * time.Second)

	rt.mu.Lock()
	active := rt.active[testID]
	if active == nil || active.ReplyFallbackSent || active.Test.ReplyAckSeenAt != nil || active.Test.ReplyEndpointAckAt != nil {
		rt.mu.Unlock()
		return
	}
	// Only meaningful when the original reply was sent direct (the inbound took
	// relays). A 0-hop reply already flooded, so there's nothing to fall back to.
	outboundHex := strVal(active.Test.OutboundHex)
	wasDirect := false
	if raw, err := hex.DecodeString(outboundHex); err == nil {
		if p, err := meshpkt.DecodePacket(raw); err == nil && p.HopCount() > 0 {
			wasDirect = true
		}
	}
	if !wasDirect {
		rt.mu.Unlock()
		return
	}
	active.ReplyFallbackSent = true
	tCopy := *active.Test
	rt.mu.Unlock()

	ep := rt.endpoint(tCopy.EndpointID)
	agent := rt.agentForEndpoint(tCopy.EndpointID)
	if ep == nil || agent == nil || !agent.IPCReady {
		return
	}
	p, err := rt.buildReplyMessage(ep, &tCopy, nil) // nil packet → flood
	if err != nil {
		return
	}
	_ = agent.send(map[string]any{"type": "sendRaw", "testId": tCopy.ID, "packetRole": "replyMessage", "rawHex": p.Hex})
	rt.noteQueuedPacket(tCopy.ID, p)
	rt.setReplyStatus(tCopy.ID, "Reply re-sent via flood (direct path unacknowledged)")
}

func (rt *Runtime) setReplyStatus(testID, status string) {
	rt.mu.Lock()
	if active := rt.active[testID]; active != nil {
		active.Test.ReplyStatus = &status
		active.Test.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		rt.publishTestLocked(active.Test)
		facts := cloneFacts(activeFacts(active.Test))
		rt.enqueueWrite(func(e dbExec) error { return rt.store.UpdateFacts(e, testID, facts) })
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
		c.send(payload)
	}
}

func (rt *Runtime) publishTest(t *Test) {
	rt.mu.RLock()
	clients := make([]*BrowserClient, 0, len(rt.browsers))
	for c := range rt.browsers {
		if c.browserID == t.BrowserID || c.subscribed(t.ID) {
			clients = append(clients, c)
		}
	}
	rt.mu.RUnlock()
	for _, c := range clients {
		c.send(map[string]any{"type": "testUpdated", "test": t})
	}
}

func (rt *Runtime) publishTestLocked(t *Test) {
	cp := publishCopy(t)
	go rt.publishTest(rt.decorate(cp))
}

func (rt *Runtime) publishObservation(t *Test, obs Observation) {
	// The observation message carries the full updated test, so there's no need
	// for a separate testUpdated broadcast — one message per packet.
	rt.mu.RLock()
	clients := make([]*BrowserClient, 0, len(rt.browsers))
	for c := range rt.browsers {
		if c.browserID == t.BrowserID || c.subscribed(t.ID) {
			clients = append(clients, c)
		}
	}
	rt.mu.RUnlock()
	for _, c := range clients {
		c.send(map[string]any{"type": "observation", "testId": t.ID, "test": t, "observation": obs})
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
	// The observer that heard this packet may be reported on the inner packet map
	// or on the data envelope depending on the analyzer; check both so each
	// reception keeps a distinct observer identity (the dedup key relies on it).
	observerID := firstNonEmpty(stringField(packet, "observer_id", "observerId"), stringField(data, "observer_id", "observerId"))
	observerName := firstNonEmpty(stringField(packet, "observer_name", "observerName"), stringField(data, "observer_name", "observerName"))
	rt.enqueuePacket(PacketEvent{
		Source: "corescope:" + source, RawHex: raw, Hash: stringField(packet, "hash"),
		ObserverID:   ptrIf(observerID),
		ObserverName: ptrIf(observerName),
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

func idListParam(r *http.Request, name string, maxItems int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range strings.Split(r.URL.Query().Get(name), ",") {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= maxItems {
			break
		}
	}
	return out
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

func isHex(value string, bytes ...int) bool {
	if value == "" {
		return false
	}
	if len(bytes) > 0 && len(value) != bytes[0]*2 {
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

func visibleTestsWhere(localIDs []string) (string, []any) {
	where := `exists (select 1 from observations o where o.test_id=tests.id)`
	args := []any{}
	if len(localIDs) == 0 {
		return where, args
	}
	qs := strings.TrimRight(strings.Repeat("?,", len(localIDs)), ",")
	where += ` or id in (` + qs + `)`
	for _, id := range localIDs {
		args = append(args, id)
	}
	return where, args
}

func (s *Store) ListVisibleTestMetas(localIDs []string, limit, offset int, eps []Endpoint) ([]Test, error) {
	where, args := visibleTestsWhere(localIDs)
	args = append(args, limit, offset)
	rows, err := s.db.Query(`select `+testColumns+`, (select count(*) from observations o where o.test_id=tests.id) from tests where `+where+` order by created_at desc limit ? offset ?`, args...)
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

func (s *Store) CountVisibleTests(localIDs []string) (int, error) {
	where, args := visibleTestsWhere(localIDs)
	var n int
	err := s.db.QueryRow(`select count(*) from tests where `+where, args...).Scan(&n)
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
	rows, err := s.db.Query(`select id,direction,source,packet_hash,observer_id,observer_name,hop_count,path_json,path_keys_json,decoded_type,route,created_at from observations where test_id=? order by created_at desc`, testID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Observation{}
	for rows.Next() {
		var o Observation
		var oid, oname, dtype, route sql.NullString
		var pathJSON, pathKeysJSON string
		if err := rows.Scan(&o.ID, &o.Direction, &o.Source, &o.PacketHash, &oid, &oname, &o.HopCount, &pathJSON, &pathKeysJSON, &dtype, &route, &o.CreatedAt); err != nil {
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
		if route.Valid {
			o.Route = &route.String
		}
		_ = json.Unmarshal([]byte(pathJSON), &o.Path)
		_ = json.Unmarshal([]byte(pathKeysJSON), &o.PathKeys)
		out = append(out, o)
	}
	return out, rows.Err()
}

// runBatch executes a set of writes inside a single transaction. Duplicate
// observations are already filtered in memory before they reach here, so the
// insert is unconditional.
func (s *Store) runBatch(jobs []func(dbExec) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[db] begin failed: %v", err)
		return
	}
	for _, job := range jobs {
		if err := job(tx); err != nil {
			log.Printf("[db] write failed: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[db] commit failed: %v", err)
		_ = tx.Rollback()
	}
}

func (s *Store) AddObservation(e dbExec, testID string, o Observation) error {
	pathJSON, _ := json.Marshal(o.Path)
	pathKeysJSON, _ := json.Marshal(o.PathKeys)
	_, err := e.Exec(`insert into observations (test_id,direction,source,packet_hash,observer_id,observer_name,hop_count,path_json,path_keys_json,decoded_type,route,created_at) values (?,?,?,?,?,?,?,?,?,?,?,?)`, testID, o.Direction, o.Source, o.PacketHash, o.ObserverID, o.ObserverName, o.HopCount, string(pathJSON), string(pathKeysJSON), o.DecodedType, o.Route, o.CreatedAt)
	return err
}

func (s *Store) UpdateFacts(e dbExec, testID string, f map[string]*string) error {
	_, err := e.Exec(`update tests set outbound_seen_at=coalesce(?,outbound_seen_at),outbound_endpoint_seen_at=coalesce(?,outbound_endpoint_seen_at),outbound_ack_seen_at=coalesce(?,outbound_ack_seen_at),reply_broadcast_at=coalesce(?,reply_broadcast_at),return_seen_at=coalesce(?,return_seen_at),reply_ack_seen_at=coalesce(?,reply_ack_seen_at),reply_endpoint_ack_at=coalesce(?,reply_endpoint_ack_at),outbound_hash=coalesce(?,outbound_hash),outbound_ack_hash=coalesce(?,outbound_ack_hash),outbound_ack_crc_hex=coalesce(?,outbound_ack_crc_hex),return_hash=coalesce(?,return_hash),reply_hash=coalesce(?,reply_hash),reply_ack_hash=coalesce(?,reply_ack_hash),reply_ack_crc_hex=coalesce(?,reply_ack_crc_hex),outbound_hex=coalesce(?,outbound_hex),outbound_ack_hex=coalesce(?,outbound_ack_hex),return_hex=coalesce(?,return_hex),reply_hex=coalesce(?,reply_hex),reply_ack_hex=coalesce(?,reply_ack_hex),reply_status=coalesce(?,reply_status),updated_at=coalesce(?,updated_at),status=coalesce(?,status) where id=?`,
		f["outbound_seen_at"], f["outbound_endpoint_seen_at"], f["outbound_ack_seen_at"], f["reply_broadcast_at"], f["return_seen_at"], f["reply_ack_seen_at"], f["reply_endpoint_ack_at"], f["outbound_hash"], f["outbound_ack_hash"], f["outbound_ack_crc_hex"], f["return_hash"], f["reply_hash"], f["reply_ack_hash"], f["reply_ack_crc_hex"], f["outbound_hex"], f["outbound_ack_hex"], f["return_hex"], f["reply_hex"], f["reply_ack_hex"], f["reply_status"], f["updated_at"], f["status"], testID)
	return err
}

func (s *Store) UpdateExpiry(e dbExec, testID, expiresAt string) error {
	_, err := e.Exec(`update tests set expires_at=? where id=?`, expiresAt, testID)
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

func (s *Store) LookupNodes(keys []string) (map[string]NodeRef, error) {
	clean := []string{}
	seen := map[string]bool{}
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, key)
	}
	if len(clean) == 0 {
		return map[string]NodeRef{}, nil
	}
	qs := strings.TrimRight(strings.Repeat("?,", len(clean)), ",")
	args := make([]any, 0, len(clean)*2)
	for _, key := range clean {
		args = append(args, key)
	}
	for _, key := range clean {
		args = append(args, key)
	}
	rows, err := s.db.Query(`select public_key,name,short_hash,lat,lon from nodes where lower(public_key) in (`+qs+`) or lower(short_hash) in (`+qs+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeRef{}
	for rows.Next() {
		var pub, name, short string
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pub, &name, &short, &lat, &lon); err != nil {
			return nil, err
		}
		node := NodeRef{Name: name, ShortHash: short, PublicKey: pub}
		if lat.Valid {
			node.Lat = &lat.Float64
		}
		if lon.Valid {
			node.Lon = &lon.Float64
		}
		out[strings.ToLower(pub)] = node
		out[strings.ToLower(short)] = node
	}
	return out, rows.Err()
}

// LookupNodeCandidates returns, for each given path hash, every node it could
// resolve to. A 1-byte hash (2 hex chars) collides across many nodes, so the
// short_hash match returns all of them; a longer hash carries enough entropy to
// match a public-key prefix instead. Candidates come back best-first (nodes with
// coordinates, then most recently seen) and capped, keyed by the lowercased hash.
func (s *Store) LookupNodeCandidates(hashes []string) (map[string][]NodeRef, error) {
	out := map[string][]NodeRef{}
	seen := map[string]bool{}
	for _, h := range hashes {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		var rows *sql.Rows
		var err error
		if len(h) <= 2 {
			rows, err = s.db.Query(`select public_key,name,short_hash,lat,lon from nodes where lower(short_hash)=? order by (lat is not null) desc, updated_at desc limit 25`, h)
		} else {
			rows, err = s.db.Query(`select public_key,name,short_hash,lat,lon from nodes where lower(public_key) like ? order by (lat is not null) desc, updated_at desc limit 25`, h+"%")
		}
		if err != nil {
			return nil, err
		}
		var list []NodeRef
		for rows.Next() {
			var pub, name, short string
			var lat, lon sql.NullFloat64
			if err := rows.Scan(&pub, &name, &short, &lat, &lon); err != nil {
				rows.Close()
				return nil, err
			}
			node := NodeRef{Name: name, ShortHash: short, PublicKey: pub}
			if lat.Valid {
				node.Lat = &lat.Float64
			}
			if lon.Valid {
				node.Lon = &lon.Float64
			}
			list = append(list, node)
		}
		closeErr := rows.Err()
		rows.Close()
		if closeErr != nil {
			return nil, closeErr
		}
		if len(list) > 0 {
			out[h] = list
		}
	}
	return out, nil
}

func (s *Store) CountNodes() (int, error) {
	var n int
	err := s.db.QueryRow(`select count(*) from nodes`).Scan(&n)
	return n, err
}

func sortTests(tests []Test) {
	sort.Slice(tests, func(i, j int) bool { return tests[i].CreatedAt > tests[j].CreatedAt })
}
