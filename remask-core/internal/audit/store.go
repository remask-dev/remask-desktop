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
	Debug                 bool   `json:"debug,omitempty"`
	RetentionDays         int    `json:"retention_days"`
	HFBaseURL             string `json:"hf_base_url,omitempty"`
	MaxInferenceTokens    int    `json:"max_inference_tokens"`
	InferenceProvider     string `json:"inference_provider"`
	EntityCacheEnabled    bool   `json:"entity_cache_enabled"`
	EntityCacheTTLSeconds int    `json:"entity_cache_ttl_seconds"`
}

func DefaultSettings() Settings {
	return Settings{RecordRequestContent: true, RetentionDays: 30, MaxInferenceTokens: 512, InferenceProvider: "cpu", EntityCacheEnabled: true, EntityCacheTTLSeconds: 900}
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
	maxAuditEntries  = 2000
	GatewayTypeAPI   = "api_gateway"
	GatewayTypeProxy = "proxy_gateway"
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

type DebugRequest struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

type DebugResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

type DebugExchange struct {
	Request  DebugRequest  `json:"request"`
	Response DebugResponse `json:"response"`
}

type Entry struct {
	ID             string         `json:"id"`
	Timestamp      time.Time      `json:"timestamp"`
	UpstreamID     string         `json:"upstream_id"`
	ProfileID      string         `json:"profile_id"`
	OperationID    string         `json:"operation_id"`
	Model          string         `json:"model,omitempty"`
	ProtectionMode string         `json:"protection_mode,omitempty"`
	GatewayType    string         `json:"gateway_type"`
	TargetHost     string         `json:"target_host,omitempty"`
	Method         string         `json:"method"`
	Path           string         `json:"path"`
	StatusCode     int            `json:"status_code"`
	DurationMS     int64          `json:"duration_ms"`
	Streaming      bool           `json:"streaming"`
	RequestBytes   int64          `json:"request_bytes"`
	ResponseBytes  int64          `json:"response_bytes"`
	EntityCount    int            `json:"entity_count"`
	TokenUsage     TokenUsage     `json:"token_usage"`
	Fields         []Field        `json:"fields,omitempty"`
	Debug          *DebugExchange `json:"debug,omitempty"`
	Extra          ExtraData      `json:"-"`
	ErrorCode      string         `json:"error_code,omitempty"`
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
	Granularity    string         `json:"granularity"`
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
		// Initialize the user configuration on first startup. Subsequent
		// launches always load settings from this file and PUT /settings keeps
		// it updated.
		if _, err := os.Stat(store.settingsPath); errors.Is(err, os.ErrNotExist) {
			if err := store.writeSettingsLocked(); err != nil {
				return nil, err
			}
		} else if err != nil {
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
			model TEXT NOT NULL DEFAULT '',
			protection_mode TEXT NOT NULL,
			gateway_type TEXT NOT NULL DEFAULT '',
			target_host TEXT NOT NULL DEFAULT '',
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
			extra_json TEXT NOT NULL DEFAULT '',
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
	if entry.GatewayType != GatewayTypeProxy {
		entry.GatewayType = GatewayTypeAPI
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
	if !settings.Debug {
		entry.Debug = nil
	}
	sortFields(entry.Fields)
	fields, err := json.Marshal(entry.Fields)
	if err != nil {
		return err
	}
	extra, err := marshalExtra(entry)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO audit_entries (
		id, timestamp, upstream_id, profile_id, operation_id, model, protection_mode, gateway_type, target_host, method, path,
		status_code, duration_ms, streaming, request_bytes, response_bytes, entity_count,
		token_input, token_output, token_total, token_cached, fields_json, extra_json, error_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.Timestamp.UTC().Format(timestampLayout), entry.UpstreamID, entry.ProfileID,
		entry.OperationID, entry.Model, entry.ProtectionMode, entry.GatewayType, entry.TargetHost, entry.Method, entry.Path, entry.StatusCode,
		entry.DurationMS, entry.Streaming, entry.RequestBytes, entry.ResponseBytes, entry.EntityCount,
		entry.TokenUsage.Input, entry.TokenUsage.Output, entry.TokenUsage.Total, entry.TokenUsage.Cached, string(fields), extra, entry.ErrorCode)
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

type ExtraData map[string]any

func marshalExtra(entry Entry) (string, error) {
	extra := ExtraData{}
	for key, value := range entry.Extra {
		extra[key] = value
	}
	if entry.Debug != nil {
		extra["request"] = entry.Debug.Request
		extra["response"] = entry.Debug.Response
	}
	if len(extra) == 0 {
		return "", nil
	}
	data, err := json.Marshal(extra)
	return string(data), err
}

func unmarshalExtra(extraJSON string) (ExtraData, *DebugExchange) {
	if extraJSON == "" {
		return nil, nil
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extraJSON), &extra); err != nil {
		return nil, nil
	}
	values := ExtraData{}
	var debug DebugExchange
	requestFound, responseFound := false, false
	for key, data := range extra {
		var value any
		if json.Unmarshal(data, &value) == nil {
			values[key] = value
		}
		switch key {
		case "request":
			requestFound = json.Unmarshal(data, &debug.Request) == nil
		case "response":
			responseFound = json.Unmarshal(data, &debug.Response) == nil
		}
	}
	if requestFound && responseFound {
		return values, &debug
	}
	return values, nil
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
		clauses = append(clauses, "LOWER(upstream_id || ' ' || profile_id || ' ' || gateway_type || ' ' || target_host || ' ' || model || ' ' || path || ' ' || error_code || ' ' || fields_json) LIKE ?")
		args = append(args, "%"+search+"%")
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id, timestamp, upstream_id, profile_id, operation_id, model,
		protection_mode, gateway_type, target_host, method, path, status_code, duration_ms, streaming, request_bytes,
		response_bytes, entity_count, token_input, token_output, token_total, token_cached, fields_json, extra_json, error_code
		FROM audit_entries WHERE `+strings.Join(clauses, " AND ")+` ORDER BY timestamp DESC LIMIT ?`, args...)
	if err != nil {
		return []Entry{}
	}
	defer rows.Close()
	result := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		var timestamp, fields, extra string
		if err := rows.Scan(&entry.ID, &timestamp, &entry.UpstreamID, &entry.ProfileID, &entry.OperationID, &entry.Model,
			&entry.ProtectionMode, &entry.GatewayType, &entry.TargetHost, &entry.Method, &entry.Path, &entry.StatusCode, &entry.DurationMS,
			&entry.Streaming, &entry.RequestBytes, &entry.ResponseBytes, &entry.EntityCount,
			&entry.TokenUsage.Input, &entry.TokenUsage.Output, &entry.TokenUsage.Total, &entry.TokenUsage.Cached, &fields, &extra, &entry.ErrorCode); err != nil {
			continue
		}
		entry.Timestamp, _ = time.Parse(timestampLayout, timestamp)
		_ = json.Unmarshal([]byte(fields), &entry.Fields)
		entry.Extra, entry.Debug = unmarshalExtra(extra)
		sortFields(entry.Fields)
		result = append(result, entry)
	}
	return result
}

// ListSummaries returns only the columns needed by the audit log list. Large
// field previews and debug exchanges stay in SQLite until a specific entry is
// opened through Get.
func (s *Store) ListSummaries(query Query) []Entry {
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
		clauses = append(clauses, "LOWER(upstream_id || ' ' || profile_id || ' ' || gateway_type || ' ' || target_host || ' ' || model || ' ' || path || ' ' || error_code || ' ' || fields_json) LIKE ?")
		args = append(args, "%"+search+"%")
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id, timestamp, upstream_id, profile_id, operation_id, model,
		protection_mode, gateway_type, target_host, method, path, status_code, duration_ms, streaming, request_bytes,
		response_bytes, entity_count, token_input, token_output, token_total, token_cached, error_code
		FROM audit_entries WHERE `+strings.Join(clauses, " AND ")+` ORDER BY timestamp DESC LIMIT ?`, args...)
	if err != nil {
		return []Entry{}
	}
	defer rows.Close()
	result := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		var timestamp string
		if err := rows.Scan(&entry.ID, &timestamp, &entry.UpstreamID, &entry.ProfileID, &entry.OperationID, &entry.Model,
			&entry.ProtectionMode, &entry.GatewayType, &entry.TargetHost, &entry.Method, &entry.Path, &entry.StatusCode, &entry.DurationMS,
			&entry.Streaming, &entry.RequestBytes, &entry.ResponseBytes, &entry.EntityCount,
			&entry.TokenUsage.Input, &entry.TokenUsage.Output, &entry.TokenUsage.Total, &entry.TokenUsage.Cached, &entry.ErrorCode); err != nil {
			continue
		}
		entry.Timestamp, _ = time.Parse(timestampLayout, timestamp)
		result = append(result, entry)
	}
	return result
}

// Get loads the complete audit entry, including field previews and any debug
// exchange. It is intentionally separate from ListSummaries so list navigation
// never pays the cost of decoding large request and response bodies.
func (s *Store) Get(id string) (Entry, bool) {
	var entry Entry
	var timestamp, fields, extra string
	err := s.db.QueryRow(`SELECT id, timestamp, upstream_id, profile_id, operation_id, model,
		protection_mode, gateway_type, target_host, method, path, status_code, duration_ms, streaming, request_bytes,
		response_bytes, entity_count, token_input, token_output, token_total, token_cached, fields_json, extra_json, error_code
		FROM audit_entries WHERE id = ?`, id).Scan(
		&entry.ID, &timestamp, &entry.UpstreamID, &entry.ProfileID, &entry.OperationID, &entry.Model,
		&entry.ProtectionMode, &entry.GatewayType, &entry.TargetHost, &entry.Method, &entry.Path, &entry.StatusCode, &entry.DurationMS,
		&entry.Streaming, &entry.RequestBytes, &entry.ResponseBytes, &entry.EntityCount,
		&entry.TokenUsage.Input, &entry.TokenUsage.Output, &entry.TokenUsage.Total, &entry.TokenUsage.Cached, &fields, &extra, &entry.ErrorCode)
	if err != nil {
		return Entry{}, false
	}
	entry.Timestamp, _ = time.Parse(timestampLayout, timestamp)
	_ = json.Unmarshal([]byte(fields), &entry.Fields)
	entry.Extra, entry.Debug = unmarshalExtra(extra)
	sortFields(entry.Fields)
	return entry, true
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
	points := make([]DailyPoint, days)
	for index := range days {
		date := start.AddDate(0, 0, index).Format("2006-01-02")
		points[index] = DailyPoint{Date: date}
	}
	return s.statsBetween(start, now.Add(time.Nanosecond), points, "day", time.UTC)
}

// StatsRange returns calendar-aligned overview data in the machine's local
// timezone. Day-sized ranges use hourly buckets; longer ranges use daily
// buckets. Today's axis covers the full calendar day, while the query's upper
// bound remains now so future audit data is never included.
func (s *Store) StatsRange(period string) Stats {
	return s.statsForRange(period, time.Now())
}

func (s *Store) statsForRange(period string, now time.Time) Stats {
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	start := today
	end := now.Add(time.Nanosecond)
	granularity := "hour"
	points := make([]DailyPoint, 0, 30)

	switch period {
	case "today":
		for cursor, tomorrow := today, today.AddDate(0, 0, 1); cursor.Before(tomorrow); cursor = cursor.Add(time.Hour) {
			points = append(points, DailyPoint{Date: cursor.Format(time.RFC3339)})
		}
	case "yesterday":
		start = today.AddDate(0, 0, -1)
		end = today
		for cursor := start; cursor.Before(end); cursor = cursor.Add(time.Hour) {
			points = append(points, DailyPoint{Date: cursor.Format(time.RFC3339)})
		}
	case "30d":
		start = today.AddDate(0, 0, -29)
		granularity = "day"
		for index := range 30 {
			points = append(points, DailyPoint{Date: start.AddDate(0, 0, index).Format("2006-01-02")})
		}
	default:
		start = today.AddDate(0, 0, -6)
		granularity = "day"
		for index := range 7 {
			points = append(points, DailyPoint{Date: start.AddDate(0, 0, index).Format("2006-01-02")})
		}
	}

	return s.statsBetween(start, end, points, granularity, location)
}

func (s *Store) statsBetween(start, end time.Time, points []DailyPoint, granularity string, location *time.Location) Stats {
	result := Stats{EntityTypes: make(map[string]int), Daily: points, Granularity: granularity}
	pointIndex := make(map[string]int, len(points))
	for index, point := range points {
		pointIndex[point.Date] = index
	}
	startText := start.UTC().Format(timestampLayout)
	endText := end.UTC().Format(timestampLayout)
	var success int
	var averageLatency float64
	var activeMinutes int
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(entity_count), 0),
		COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(duration_ms), 0), COALESCE(SUM(streaming), 0),
		COALESCE(SUM(token_input), 0), COALESCE(SUM(token_output), 0),
		COALESCE(SUM(token_total), 0), COALESCE(SUM(token_cached), 0),
		COUNT(DISTINCT substr(timestamp, 1, 16))
		FROM audit_entries WHERE timestamp >= ? AND timestamp < ?`, startText, endText).Scan(
		&result.Requests, &result.Entities, &success, &averageLatency, &result.Streaming,
		&result.TokenInput, &result.TokenOutput, &result.TokenTotal, &result.TokenCached, &activeMinutes)
	result.AverageLatency = int64(averageLatency)
	if result.Requests > 0 {
		result.SuccessRate = float64(success) / float64(result.Requests)
	}
	if activeMinutes > 0 {
		result.TokensPerMin = float64(result.TokenTotal) / float64(activeMinutes)
	}
	rows, err := s.db.Query(`SELECT timestamp, entity_count
		FROM audit_entries WHERE timestamp >= ? AND timestamp < ?`, startText, endText)
	if err == nil {
		for rows.Next() {
			var timestamp string
			var entities int
			if rows.Scan(&timestamp, &entities) == nil {
				parsed, parseErr := time.Parse(timestampLayout, timestamp)
				if parseErr != nil {
					continue
				}
				local := parsed.In(location)
				key := local.Format("2006-01-02")
				if granularity == "hour" {
					key = local.Format("2006-01-02T15:00:00Z07:00")
				}
				if index, ok := pointIndex[key]; ok {
					result.Daily[index].Requests++
					result.Daily[index].Entities += entities
				}
			}
		}
		rows.Close()
	}
	entityRows, err := s.db.Query(`SELECT t.entity_type, COALESCE(SUM(t.count), 0)
		FROM audit_entity_types t JOIN audit_entries e ON e.id = t.entry_id
		WHERE e.timestamp >= ? AND e.timestamp < ? GROUP BY t.entity_type`, startText, endText)
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
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
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
