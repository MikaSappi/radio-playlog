package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

type Configuration struct {
	UserID       string `json:"uid"`
	InstanceID   string `json:"instance_id"`
	UserLogDir   string `json:"log_dir"`
	ArchiveDir   string `json:"arch_dir"`
	IsInGCP      bool   `json:"use_gcp"`
	GCPProject   string `json:"gcp_project"`
	BQDataset    string `json:"bq_dataset"`
	BQTable      string `json:"bq_table"`
	AuthKeysFile string `json:"auth_keys_file"`
}

type PlaylogEntry struct {
	Timestamp time.Time `bigquery:"timestamp"`
	UserID    string    `bigquery:"user_id"`
	Title     string    `bigquery:"title"`
	Artist    string    `bigquery:"artist"`
	KeyHash   string    `bigquery:"key_hash"`
	Label     string    `bigquery:"label"`
}

// writeFunc is the storage-agnostic write entrypoint.
type writeFunc func(PlaylogEntry) error

// keyInfo is what a presented API key resolves to: the owning user and the
// label snapshot to stamp onto each play. The label is resolved at log time
// so rename history doesn't rewrite past rows.
type keyInfo struct {
	UserID string
	Label  string
}

// authFunc resolves an API key to the owning user + current label. ok=false
// means unknown/missing key when a key is required.
type authFunc func(apiKey string) (keyInfo, string, bool) // returns (info, keyHashHex, ok)

func (c *Configuration) loadConfig(path string) {
	confData, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Can't read config %s: %v", path, err)
	}

	if err := json.Unmarshal(confData, &c); err != nil {
		log.Fatalf("Can't parse config %s: %v", path, err)
	}
}

// resolveConfigPath picks the configuration.json location from (in order):
// the -config flag, the RADIO_PLAYLOG_CONFIG env var, then ./configuration.json.
// This lets the same binary ship to Cloud Run, a datacenter systemd unit, or
// a user's laptop without any repackaging.
func resolveConfigPath() string {
	var flagPath string
	flag.StringVar(&flagPath, "config", "", "path to configuration.json")
	flag.Parse()

	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("RADIO_PLAYLOG_CONFIG"); env != "" {
		return env
	}
	return "configuration.json"
}

func main() {
	configPath := resolveConfigPath()
	log.Printf("Loading config from %s", configPath)

	var c Configuration
	c.loadConfig(configPath)

	if c.IsInGCP {
		runGCP(&c)
	} else {
		runLocal(&c)
	}
}

// ---- run modes ----

func runGCP(c *Configuration) {
	if c.GCPProject == "" || c.BQDataset == "" || c.BQTable == "" {
		log.Fatal("GCP mode requires gcp_project, bq_dataset, and bq_table in configuration.json")
	}

	ctx := context.Background()
	client, err := bigquery.NewClient(ctx, c.GCPProject)
	if err != nil {
		log.Fatalf("BigQuery client init failed: %v", err)
	}
	defer client.Close()

	inserter := client.Dataset(c.BQDataset).Table(c.BQTable).Inserter()
	write := func(e PlaylogEntry) error { return inserter.Put(ctx, e) }

	load := func() (map[string]keyInfo, error) {
		return loadBQKeys(ctx, client, c.GCPProject, c.BQDataset, "api_keys")
	}
	initial, err := load()
	if err != nil {
		log.Fatalf("failed to load api_keys table: %v", err)
	}
	store := &keyStore{keys: initial}
	go store.startRefresh(load, 5*time.Minute)
	log.Printf("GCP mode: plays=%s.%s.%s, %d api keys loaded",
		c.GCPProject, c.BQDataset, c.BQTable, len(initial))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleHTTPLog(w, r, write, store.Lookup)
	})

	log.Printf("HTTP server listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

func runLocal(c *Configuration) {
	dataDir := c.UserLogDir
	archiveDir := c.ArchiveDir

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		log.Fatal(err)
	}

	write := func(e PlaylogEntry) error { return writeLocalCSV(e, dataDir) }

	var auth authFunc
	multiUser := c.AuthKeysFile != ""
	if multiUser {
		load := func() (map[string]keyInfo, error) { return loadFileKeys(c.AuthKeysFile) }
		initial, err := load()
		if err != nil {
			log.Fatalf("failed to load %s: %v", c.AuthKeysFile, err)
		}
		store := &keyStore{keys: initial}
		go store.startRefresh(load, 5*time.Minute)
		auth = store.Lookup
		log.Printf("Local multi-user mode: dataDir=%s archiveDir=%s, %d keys loaded from %s",
			dataDir, archiveDir, len(initial), c.AuthKeysFile)
	} else {
		if c.UserID == "" {
			log.Fatal("local single-user mode requires uid in configuration.json")
		}
		configUID := c.UserID
		// Single-user passthrough: no hash, no label.
		auth = func(_ string) (keyInfo, string, bool) {
			return keyInfo{UserID: configUID}, "", true
		}
		log.Printf("Local single-user mode: dataDir=%s archiveDir=%s uid=%s",
			dataDir, archiveDir, configUID)
	}

	go archiveWatcher(dataDir, archiveDir)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleHTTPLog(w, r, write, auth)
	})

	if multiUser {
		// Public HTTP only. Front with Caddy/nginx for TLS. No TCP — TCP is
		// for loopback convenience and doesn't carry an API key header.
		log.Printf("HTTP server listening on port 9201")
		if err := http.ListenAndServe(":9201", nil); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
		return
	}

	// Single-user: HTTP in background, TCP in foreground for loopback use.
	go func() {
		log.Printf("HTTP server listening on port 9201")
		if err := http.ListenAndServe(":9201", nil); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	listener, err := net.Listen("tcp", ":9200")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("TCP server listening on port 9200")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept error: %v", err)
			continue
		}
		go handleConnection(conn, write, auth)
	}
}

// ---- HTTP/TCP handlers ----

func handleHTTPLog(w http.ResponseWriter, r *http.Request, write writeFunc, auth authFunc) {
	log.Printf("HTTP request received: method=%s path=%s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)

	if r.Method != http.MethodPost {
		log.Printf("Rejected non-POST method: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info, keyHashHex, ok := auth(r.Header.Get("X-API-Key"))
	if !ok {
		log.Printf("Rejected request with missing/invalid X-API-Key")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Failed to read body: %v", err)
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		log.Printf("Rejected empty message")
		http.Error(w, "Empty message", http.StatusBadRequest)
		return
	}

	entry, err := parseEntry(body, info.UserID, keyHashHex, info.Label)
	if err != nil {
		log.Printf("Parse error: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := write(entry); err != nil {
		log.Printf("Write error: %v", err)
		http.Error(w, "Failed to write log", http.StatusInternalServerError)
		return
	}

	log.Printf("Logged message for user=%s key=%s label=%q", info.UserID, shortHash(keyHashHex), info.Label)
	w.WriteHeader(http.StatusOK)
}

func handleConnection(conn net.Conn, write writeFunc, auth authFunc) {
	defer conn.Close()

	log.Printf("TCP connection from: %s", conn.RemoteAddr())

	// TCP has no headers — rely on the single-user passthrough auth.
	info, keyHashHex, ok := auth("")
	if !ok {
		log.Printf("TCP rejected: auth returned not-ok")
		return
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		line := scanner.Bytes()
		log.Printf("TCP received (length=%d)", len(line))
		if len(line) == 0 {
			return
		}
		entry, err := parseEntry(line, info.UserID, keyHashHex, info.Label)
		if err != nil {
			log.Printf("TCP parse error: %v", err)
			return
		}
		if err := write(entry); err != nil {
			log.Printf("TCP log error: %v", err)
			return
		}
		log.Printf("Logged TCP message for user=%s", info.UserID)
	}
}

func parseEntry(body []byte, userID, keyHash, label string) (PlaylogEntry, error) {
	var data map[string]string
	if err := json.Unmarshal(body, &data); err != nil {
		return PlaylogEntry{}, fmt.Errorf("failed to parse JSON: %v", err)
	}
	return PlaylogEntry{
		Timestamp: time.Now().UTC(),
		UserID:    userID,
		Title:     data["title"],
		Artist:    data["artist"],
		KeyHash:   keyHash,
		Label:     label,
	}, nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ---- key store ----

type apiKeyEntry struct {
	KeyHash string `json:"key_hash" bigquery:"key_hash"`
	UserID  string `json:"user_id"  bigquery:"user_id"`
	Enabled bool   `json:"enabled"  bigquery:"enabled"`
	// Label is the human-friendly name the user gave this key (station
	// name). The logger snapshots it into each play row so renames don't
	// rewrite history. Nullable to stay compatible with older rows.
	Label bigquery.NullString `json:"label" bigquery:"label"`
}

type keyStore struct {
	mu   sync.RWMutex
	keys map[string]keyInfo // lowercase hex SHA256 -> keyInfo
}

func (s *keyStore) set(next map[string]keyInfo) {
	s.mu.Lock()
	s.keys = next
	s.mu.Unlock()
}

// Lookup resolves an API key. Returns the keyInfo for the key, the
// lowercase-hex SHA256 of the key (so callers can tag their writes), and
// ok=true if the key is known and enabled.
func (s *keyStore) Lookup(apiKey string) (keyInfo, string, bool) {
	if apiKey == "" {
		return keyInfo{}, "", false
	}
	sum := sha256.Sum256([]byte(apiKey))
	hexHash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.keys[hexHash]
	return info, hexHash, ok
}

func (s *keyStore) startRefresh(load func() (map[string]keyInfo, error), interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		m, err := load()
		if err != nil {
			log.Printf("key reload failed: %v", err)
			continue
		}
		s.set(m)
		log.Printf("Reloaded %d api keys", len(m))
	}
}

// fileKeyEntry mirrors apiKeyEntry but uses a plain string for Label so the
// on-disk JSON stays ergonomic for humans.
type fileKeyEntry struct {
	KeyHash string `json:"key_hash"`
	UserID  string `json:"user_id"`
	Enabled bool   `json:"enabled"`
	Label   string `json:"label"`
}

func loadFileKeys(path string) (map[string]keyInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []fileKeyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	out := make(map[string]keyInfo, len(entries))
	for _, e := range entries {
		if !e.Enabled || e.UserID == "" || e.KeyHash == "" {
			continue
		}
		out[strings.ToLower(e.KeyHash)] = keyInfo{UserID: e.UserID, Label: e.Label}
	}
	return out, nil
}

func loadBQKeys(ctx context.Context, client *bigquery.Client, project, dataset, table string) (map[string]keyInfo, error) {
	query := fmt.Sprintf("SELECT key_hash, user_id, enabled, label FROM `%s.%s.%s` WHERE enabled = TRUE",
		project, dataset, table)
	it, err := client.Query(query).Read(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]keyInfo)
	for {
		var row apiKeyEntry
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		if row.UserID == "" || row.KeyHash == "" {
			continue
		}
		label := ""
		if row.Label.Valid {
			label = row.Label.StringVal
		}
		out[strings.ToLower(row.KeyHash)] = keyInfo{UserID: row.UserID, Label: label}
	}
	return out, nil
}

// ---- local CSV storage ----

func writeLocalCSV(entry PlaylogEntry, dataDir string) error {
	filename := filepath.Join(dataDir, entry.Timestamp.Format("2006-01")+".csv")

	fileExists := true
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		fileExists = false
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if !fileExists {
		if _, err := f.WriteString("timestamp,user_id,title,artist,key_hash,label\n"); err != nil {
			return err
		}
	}

	userID := escapeCSV(entry.UserID)
	title := escapeCSV(entry.Title)
	artist := escapeCSV(entry.Artist)
	keyHash := escapeCSV(entry.KeyHash)
	label := escapeCSV(entry.Label)

	logEntry := fmt.Sprintf("%s,%s,%s,%s,%s,%s\n",
		entry.Timestamp.Format(time.RFC3339), userID, title, artist, keyHash, label)
	_, err = f.WriteString(logEntry)
	return err
}

func escapeCSV(field string) string {
	if strings.ContainsAny(field, ",\"\n") {
		field = strings.ReplaceAll(field, "\"", "\"\"")
		return fmt.Sprintf("\"%s\"", field)
	}
	return field
}

// ---- monthly archive ----

func archiveWatcher(dataDir, archiveDir string) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	lastMonth := time.Now().Format("2006-01")

	for range ticker.C {
		currentMonth := time.Now().Format("2006-01")

		if currentMonth != lastMonth {
			archiveMonth(lastMonth, dataDir, archiveDir)
			lastMonth = currentMonth
		}
	}
}

func archiveMonth(month string, dataDir, archiveDir string) {
	filename := filepath.Join(dataDir, month+".csv")

	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return
	}

	zipPath := filepath.Join(archiveDir, month+".zip")

	zipFile, err := os.Create(zipPath)
	if err != nil {
		log.Printf("Failed to create zip: %v", err)
		return
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	file, err := os.Open(filename)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		return
	}
	defer file.Close()

	writer, err := archive.Create(filepath.Base(filename))
	if err != nil {
		log.Printf("Failed to create zip entry: %v", err)
		return
	}

	if _, err := io.Copy(writer, file); err != nil {
		log.Printf("Failed to write to zip: %v", err)
		return
	}

	archive.Close()
	zipFile.Close()
	file.Close()

	if err := os.Remove(filename); err != nil {
		log.Printf("Failed to remove original file: %v", err)
	}

	log.Printf("Archived %s to %s", filename, zipPath)
}
