package audit

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type Settings struct {
	RecordRequestContent  bool   `json:"record_request_content"`
	RetentionDays         int    `json:"retention_days"`
	HFBaseURL             string `json:"hf_base_url,omitempty"`
	MaxInferenceTokens    int    `json:"max_inference_tokens"`
	InferenceProvider     string `json:"inference_provider"`
	EntityCacheEnabled    bool   `json:"entity_cache_enabled"`
	EntityCacheTTLSeconds int    `json:"entity_cache_ttl_seconds"`
}

func DefaultSettings() Settings {
	return Settings{RecordRequestContent: true, RetentionDays: 30, MaxInferenceTokens: 512, InferenceProvider: "cpu", EntityCacheEnabled: true, EntityCacheTTLSeconds: 300}
}

func (s Settings) Validate() error {
	if s.RetentionDays < 1 || s.RetentionDays > 365 {
		return errors.New("retention_days must be between 1 and 365")
	}
	if s.MaxInferenceTokens < 512 || s.MaxInferenceTokens > 4096 {
		return errors.New("max_inference_tokens must be between 512 and 4096")
	}
	if s.EntityCacheTTLSeconds < 1 || s.EntityCacheTTLSeconds > 86400 {
		return errors.New("entity_cache_ttl_seconds must be between 1 and 86400")
	}
	provider := strings.ToLower(strings.TrimSpace(s.InferenceProvider))
	if provider == "" {
		provider = "auto"
	}
	if provider != "auto" && provider != "cpu" && provider != "gpu" {
		return errors.New("inference_provider must be auto, cpu, or gpu")
	}
	if strings.TrimSpace(s.HFBaseURL) != "" {
		if !strings.HasPrefix(strings.TrimRight(s.HFBaseURL, "/"), "https://") && !strings.HasPrefix(strings.TrimRight(s.HFBaseURL, "/"), "http://") {
			return errors.New("hf_base_url must be an http or https URL")
		}
	}
	return nil
}

const (
	maxAuditEntries = 2000
)

type Entity struct {
	Type        string   `json:"type"`
	Replacement string   `json:"replacement"`
	Masked      string   `json:"masked,omitempty"`
	Confidence  float64  `json:"confidence"`
	Sources     []string `json:"sources,omitempty"`
}

type TokenUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Total  int `json:"total"`
	Cached int `json:"cached"`
}

type Field struct {
	Path           string   `json:"path"`
	OriginalMasked string   `json:"original_masked"`
	Redacted       string   `json:"redacted"`
	Entities       []Entity `json:"entities"`
}

type Entry struct {
	ID             string     `json:"id"`
	Timestamp      time.Time  `json:"timestamp"`
	UpstreamID     string     `json:"upstream_id"`
	ProfileID      string     `json:"profile_id"`
	OperationID    string     `json:"operation_id"`
	ProtectionMode string     `json:"protection_mode,omitempty"`
	Method         string     `json:"method"`
	Path           string     `json:"path"`
	StatusCode     int        `json:"status_code"`
	DurationMS     int64      `json:"duration_ms"`
	Streaming      bool       `json:"streaming"`
	RequestBytes   int64      `json:"request_bytes"`
	ResponseBytes  int64      `json:"response_bytes"`
	EntityCount    int        `json:"entity_count"`
	TokenUsage     TokenUsage `json:"token_usage"`
	Fields         []Field    `json:"fields,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
}

type Query struct {
	Limit      int
	UpstreamID string
	Status     string
	Search     string
}

type DailyPoint struct {
	Date     string `json:"date"`
	Requests int    `json:"requests"`
	Entities int    `json:"entities"`
}

type Stats struct {
	Requests       int            `json:"requests"`
	Entities       int            `json:"entities"`
	SuccessRate    float64        `json:"success_rate"`
	AverageLatency int64          `json:"average_latency_ms"`
	Streaming      int            `json:"streaming_requests"`
	EntityTypes    map[string]int `json:"entity_types"`
	Daily          []DailyPoint   `json:"daily"`
	TokenInput     int            `json:"token_input"`
	TokenOutput    int            `json:"token_output"`
	TokenTotal     int            `json:"token_total"`
	TokenCached    int            `json:"token_cached"`
	TokensPerMin   float64        `json:"tokens_per_minute"`
}

type Store struct {
	mu           sync.RWMutex
	db           *sql.DB
	settings     Settings
	settingsPath string
}

func NewStore(dataDir string) (*Store, error) {
	store := &Store{settings: DefaultSettings()}
	dsn := ":memory:"
	if strings.TrimSpace(dataDir) != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
		store.settingsPath = filepath.Join(dataDir, "settings.json")
		dsn = filepath.Join(dataDir, "remask.db")
		file, err := os.OpenFile(dsn, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		if err := os.Chmod(dsn, 0o600); err != nil {
			return nil, err
		}
		if err := store.loadSettings(); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store.db = db
	if err := store.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	if dsn != ":memory:" {
		for _, path := range []string{dsn, dsn + "-wal", dsn + "-shm"} {
			if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				db.Close()
				return nil, err
			}
		}
	}
	if err := store.prune(time.Now().UTC()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize() error {
	_, err := s.db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA foreign_keys = ON;
		CREATE TABLE IF NOT EXISTS audit_entries (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			upstream_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			protection_mode TEXT NOT NULL,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			streaming INTEGER NOT NULL,
			request_bytes INTEGER NOT NULL,
			response_bytes INTEGER NOT NULL,
			entity_count INTEGER NOT NULL,
			token_input INTEGER NOT NULL,
			token_output INTEGER NOT NULL,
			token_total INTEGER NOT NULL,
			token_cached INTEGER NOT NULL DEFAULT 0,
			fields_json TEXT NOT NULL,
			error_code TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_audit_entries_timestamp ON audit_entries(timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_audit_entries_upstream ON audit_entries(upstream_id, timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_audit_entries_status ON audit_entries(status_code, timestamp DESC);
		CREATE TABLE IF NOT EXISTS audit_entity_types (
			entry_id TEXT NOT NULL REFERENCES audit_entries(id) ON DELETE CASCADE,
			entity_type TEXT NOT NULL,
			count INTEGER NOT NULL,
			PRIMARY KEY (entry_id, entity_type)
		);
		CREATE INDEX IF NOT EXISTS idx_audit_entity_types_type ON audit_entity_types(entity_type);
	`)
	if err != nil {
		return err
	}
	// Additive schema update for databases created before cached-token logging.
	// SQLite has no IF NOT EXISTS variant for ADD COLUMN, so ignore the
	// duplicate-column error while preserving all existing audit records.
	_, alterErr := s.db.Exec(`ALTER TABLE audit_entries ADD COLUMN token_cached INTEGER NOT NULL DEFAULT 0`)
	if alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column") {
		return alterErr
	}
	return nil
}

func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Store) Configure(settings Settings) error {
	if settings.EntityCacheTTLSeconds == 0 {
		settings.EntityCacheTTLSeconds = DefaultSettings().EntityCacheTTLSeconds
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = settings
	err := s.writeSettingsLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.prune(time.Now().UTC())
}

func (s *Store) Add(entry Entry) error {
	if entry.ID == "" {
		entry.ID = "req_" + randomID()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	// Request logs are always recorded as metadata. The content toggle only
	// controls whether masked field previews and AI-bound payloads are kept;
	// aggregate counters such as entity_count stay intact for statistics.
	s.mu.RLock()
	settings := s.settings
	s.mu.RUnlock()
	if !settings.RecordRequestContent {
		entry.Fields = nil
	}
	sortFields(entry.Fields)
	fields, err := json.Marshal(entry.Fields)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO audit_entries (
		id, timestamp, upstream_id, profile_id, operation_id, protection_mode, method, path,
		status_code, duration_ms, streaming, request_bytes, response_bytes, entity_count,
		token_input, token_output, token_total, token_cached, fields_json, error_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Timestamp.UTC().Format(timestampLayout), entry.UpstreamID, entry.ProfileID,
		entry.OperationID, entry.ProtectionMode, entry.Method, entry.Path, entry.StatusCode,
		entry.DurationMS, entry.Streaming, entry.RequestBytes, entry.ResponseBytes, entry.EntityCount,
		entry.TokenUsage.Input, entry.TokenUsage.Output, entry.TokenUsage.Total, entry.TokenUsage.Cached, string(fields), entry.ErrorCode)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, field := range entry.Fields {
		for _, entity := range field.Entities {
			counts[entity.Type]++
		}
	}
	for entityType, count := range counts {
		if _, err := tx.Exec(`INSERT INTO audit_entity_types(entry_id, entity_type, count) VALUES (?, ?, ?)`, entry.ID, entityType, count); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.prune(time.Now().UTC())
}

func (s *Store) List(query Query) []Entry {
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	clauses := []string{"1 = 1"}
	args := make([]any, 0, 5)
	if query.UpstreamID != "" {
		clauses = append(clauses, "upstream_id = ?")
		args = append(args, query.UpstreamID)
	}
	if query.Status == "success" {
		clauses = append(clauses, "status_code >= 200 AND status_code < 400")
	} else if query.Status == "error" {
		clauses = append(clauses, "(status_code < 200 OR status_code >= 400)")
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		clauses = append(clauses, "LOWER(upstream_id || ' ' || profile_id || ' ' || path || ' ' || error_code || ' ' || fields_json) LIKE ?")
		args = append(args, "%"+search+"%")
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id, timestamp, upstream_id, profile_id, operation_id,
		protection_mode, method, path, status_code, duration_ms, streaming, request_bytes,
		response_bytes, entity_count, token_input, token_output, token_total, token_cached, fields_json, error_code
		FROM audit_entries WHERE `+strings.Join(clauses, " AND ")+` ORDER BY timestamp DESC LIMIT ?`, args...)
	if err != nil {
		return []Entry{}
	}
	defer rows.Close()
	result := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		var timestamp, fields string
		if err := rows.Scan(&entry.ID, &timestamp, &entry.UpstreamID, &entry.ProfileID, &entry.OperationID,
			&entry.ProtectionMode, &entry.Method, &entry.Path, &entry.StatusCode, &entry.DurationMS,
			&entry.Streaming, &entry.RequestBytes, &entry.ResponseBytes, &entry.EntityCount,
			&entry.TokenUsage.Input, &entry.TokenUsage.Output, &entry.TokenUsage.Total, &entry.TokenUsage.Cached, &fields, &entry.ErrorCode); err != nil {
			continue
		}
		entry.Timestamp, _ = time.Parse(timestampLayout, timestamp)
		_ = json.Unmarshal([]byte(fields), &entry.Fields)
		sortFields(entry.Fields)
		result = append(result, entry)
	}
	return result
}

func (s *Store) Clear() error {
	_, err := s.db.Exec(`DELETE FROM audit_entries`)
	return err
}

func (s *Store) Stats(days int) Stats {
	if days < 1 || days > 365 {
		days = 7
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
	result := Stats{EntityTypes: make(map[string]int), Daily: make([]DailyPoint, days)}
	dailyIndex := make(map[string]int, days)
	for index := range days {
		date := start.AddDate(0, 0, index).Format("2006-01-02")
		result.Daily[index] = DailyPoint{Date: date}
		dailyIndex[date] = index
	}
	startText := start.Format(timestampLayout)
	var success int
	var averageLatency float64
	var activeMinutes int
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(entity_count), 0),
		COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(duration_ms), 0), COALESCE(SUM(streaming), 0),
		COALESCE(SUM(token_input), 0), COALESCE(SUM(token_output), 0),
		COALESCE(SUM(token_total), 0), COALESCE(SUM(token_cached), 0),
		COUNT(DISTINCT substr(timestamp, 1, 16))
		FROM audit_entries WHERE timestamp >= ?`, startText).Scan(
		&result.Requests, &result.Entities, &success, &averageLatency, &result.Streaming,
		&result.TokenInput, &result.TokenOutput, &result.TokenTotal, &result.TokenCached, &activeMinutes)
	result.AverageLatency = int64(averageLatency)
	if result.Requests > 0 {
		result.SuccessRate = float64(success) / float64(result.Requests)
	}
	if activeMinutes > 0 {
		result.TokensPerMin = float64(result.TokenTotal) / float64(activeMinutes)
	}
	rows, err := s.db.Query(`SELECT substr(timestamp, 1, 10), COUNT(*), COALESCE(SUM(entity_count), 0)
		FROM audit_entries WHERE timestamp >= ? GROUP BY substr(timestamp, 1, 10)`, startText)
	if err == nil {
		for rows.Next() {
			var date string
			var requests, entities int
			if rows.Scan(&date, &requests, &entities) == nil {
				if index, ok := dailyIndex[date]; ok {
					result.Daily[index].Requests = requests
					result.Daily[index].Entities = entities
				}
			}
		}
		rows.Close()
	}
	entityRows, err := s.db.Query(`SELECT t.entity_type, COALESCE(SUM(t.count), 0)
		FROM audit_entity_types t JOIN audit_entries e ON e.id = t.entry_id
		WHERE e.timestamp >= ? GROUP BY t.entity_type`, startText)
	if err == nil {
		for entityRows.Next() {
			var entityType string
			var count int
			if entityRows.Scan(&entityType, &count) == nil {
				result.EntityTypes[entityType] = count
			}
		}
		entityRows.Close()
	}
	return result
}

func (s *Store) prune(now time.Time) error {
	s.mu.RLock()
	settings := s.settings
	s.mu.RUnlock()
	cutoff := now.AddDate(0, 0, -settings.RetentionDays).Format(timestampLayout)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM audit_entries WHERE timestamp < ?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM audit_entries WHERE id IN (
		SELECT id FROM audit_entries ORDER BY timestamp DESC LIMIT -1 OFFSET ?
	)`, maxAuditEntries); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) loadSettings() error {
	data, err := os.ReadFile(s.settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Migrate the pre-content-toggle setting name (record_request_logs) so a
	// user who disabled logging before this rename keeps their choice.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, exists := raw["record_request_content"]; !exists {
		if legacy, ok := raw["record_request_logs"].(bool); ok {
			raw["record_request_content"] = legacy
		}
		delete(raw, "record_request_logs")
		migrated, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		data = migrated
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	if settings.MaxInferenceTokens == 0 {
		settings.MaxInferenceTokens = DefaultSettings().MaxInferenceTokens
	}
	if _, exists := raw["entity_cache_enabled"]; !exists {
		settings.EntityCacheEnabled = DefaultSettings().EntityCacheEnabled
	}
	if _, exists := raw["entity_cache_ttl_seconds"]; !exists || settings.EntityCacheTTLSeconds == 0 {
		settings.EntityCacheTTLSeconds = DefaultSettings().EntityCacheTTLSeconds
	}
	if strings.TrimSpace(settings.InferenceProvider) == "" {
		settings.InferenceProvider = DefaultSettings().InferenceProvider
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	s.settings = settings
	return nil
}

func (s *Store) writeSettingsLocked() error {
	if s.settingsPath == "" {
		return nil
	}
	return atomicJSON(s.settingsPath, s.settings)
}

func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func randomID() string {
	buffer := make([]byte, 10)
	if _, err := rand.Read(buffer); err != nil {
		return strings.Repeat("0", 20)
	}
	return hex.EncodeToString(buffer)
}
