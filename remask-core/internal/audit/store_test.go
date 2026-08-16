package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsAndAggregatesMaskedAudit(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{
		Timestamp: time.Now().UTC(), UpstreamID: "openai", ProfileID: "openai",
		OperationID: "create-chat-completion", Model: "gpt-4.1-mini", Method: "POST", Path: "/v1/chat/completions",
		TargetHost: "api.openai.com",
		StatusCode: 200, DurationMS: 18, EntityCount: 1, TokenUsage: TokenUsage{Input: 12, Output: 8, Total: 20, Cached: 6},
		Fields: []Field{{Path: "/messages/0/content", OriginalMasked: "联系 [PHONE_NUMBER]", Redacted: "联系 <PHONE_NUMBER:A7F2>", Entities: []Entity{{Type: "PHONE_NUMBER", Replacement: "<PHONE_NUMBER:A7F2>"}}}},
	}
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "remask.db")); err != nil {
		t.Fatalf("remask.db was not created: %v", err)
	}
	for _, legacy := range []string{"audit.db", "audit.jsonl"} {
		if _, err := os.Stat(filepath.Join(directory, legacy)); !os.IsNotExist(err) {
			t.Fatalf("legacy %s should not be used: %v", legacy, err)
		}
	}
	loaded, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	logs := loaded.List(Query{Limit: 10})
	if len(logs) != 1 || strings.Contains(logs[0].Fields[0].OriginalMasked, "13800138000") {
		t.Fatalf("unexpected logs: %#v", logs)
	}
	if logs[0].TokenUsage.Cached != 6 {
		t.Fatalf("cached token usage was not persisted: %#v", logs[0].TokenUsage)
	}
	if logs[0].Model != "gpt-4.1-mini" {
		t.Fatalf("request model was not persisted: %#v", logs[0])
	}
	if logs[0].GatewayType != GatewayTypeAPI {
		t.Fatalf("default gateway type was not persisted: %#v", logs[0])
	}
	if logs[0].TargetHost != "api.openai.com" {
		t.Fatalf("target host was not persisted: %#v", logs[0])
	}
	if matched := loaded.List(Query{Limit: 10, Search: "gpt-4.1-mini"}); len(matched) != 1 {
		t.Fatalf("request model was not searchable: %#v", matched)
	}
	stats := loaded.Stats(7)
	if stats.Requests != 1 || stats.Entities != 1 || stats.EntityTypes["PHONE_NUMBER"] != 1 || stats.SuccessRate != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestStoreInitializesSettingsFile(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"debug"`) {
		t.Fatalf("default debug setting should not be written: %s", data)
	}
	if got := store.Settings(); got.Debug {
		t.Fatalf("debug should be disabled by default: %#v", got)
	}
}

func TestStoreRejectsIncompletePersistedSettings(t *testing.T) {
	directory := t.TempDir()
	data := []byte(`{"record_request_content":true,"retention_days":30}`)
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(directory); err == nil {
		t.Fatal("incomplete settings must not be filled from defaults")
	}
}

func TestStoreUsesOneGenericExtraColumn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`PRAGMA table_info(audit_entries)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if !columns["extra_json"] || !columns["gateway_type"] || !columns["target_host"] || columns["debug_request_json"] || columns["debug_response_json"] {
		t.Fatalf("unexpected audit extra columns: %#v", columns)
	}
}

func TestStorePersistsProxyGatewayTypeInSummariesAndDetails(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Entry{UpstreamID: "test", GatewayType: GatewayTypeProxy, TargetHost: "api.example.com"}); err != nil {
		t.Fatal(err)
	}
	summaries := store.ListSummaries(Query{Limit: 1})
	if len(summaries) != 1 || summaries[0].GatewayType != GatewayTypeProxy || summaries[0].TargetHost != "api.example.com" {
		t.Fatalf("proxy gateway type missing from summary: %#v", summaries)
	}
	detail, ok := store.Get(summaries[0].ID)
	if !ok || detail.GatewayType != GatewayTypeProxy || detail.TargetHost != "api.example.com" {
		t.Fatalf("proxy gateway type missing from detail: %#v", detail)
	}
}

func TestStoreWritesDebugExchangeDirectlyToExtraColumn(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.Debug = true
	if err := store.Configure(settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Entry{Debug: &DebugExchange{
		Request:  DebugRequest{Method: "POST", URL: "/v1/chat/completions"},
		Response: DebugResponse{Status: 200},
	}}); err != nil {
		t.Fatal(err)
	}
	var extra string
	if err := store.db.QueryRow(`SELECT extra_json FROM audit_entries LIMIT 1`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(extra, `"debug"`) || !strings.Contains(extra, `"request"`) || !strings.Contains(extra, `"response"`) {
		t.Fatalf("debug exchange should be stored directly in extra_json: %s", extra)
	}
	loaded := store.List(Query{Limit: 1})
	if len(loaded) != 1 || loaded[0].Debug == nil || loaded[0].Debug.Response.Status != 200 {
		t.Fatalf("debug exchange was not decoded from extra_json: %#v", loaded)
	}
}

func TestStoreListsMetadataAndLoadsContentOnDemand(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.Debug = true
	if err := store.Configure(settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Entry{
		UpstreamID: "openai",
		Fields:     []Field{{Path: "/messages/0/content", OriginalMasked: "masked", Redacted: "redacted"}},
		Debug: &DebugExchange{
			Request:  DebugRequest{Method: "POST", Body: "complete request"},
			Response: DebugResponse{Status: 200, Body: "complete response"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	summaries := store.ListSummaries(Query{Limit: 10})
	if len(summaries) != 1 {
		t.Fatalf("summaries = %#v", summaries)
	}
	if len(summaries[0].Fields) != 0 || summaries[0].Debug != nil || summaries[0].Extra != nil {
		t.Fatalf("list summary loaded detail content: %#v", summaries[0])
	}
	detail, ok := store.Get(summaries[0].ID)
	if !ok || len(detail.Fields) != 1 || detail.Debug == nil || detail.Debug.Response.Body != "complete response" {
		t.Fatalf("detail = %#v, found = %v", detail, ok)
	}
	if _, ok := store.Get("missing"); ok {
		t.Fatal("missing audit entry was reported as found")
	}
}

func TestStoreKeepsMetadataWhenContentDisabled(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.RecordRequestContent = false
	if err := store.Configure(settings); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Entry{
		UpstreamID: "openai", ProfileID: "openai", StatusCode: 200, EntityCount: 2,
		Fields: []Field{{Path: "/messages/0/content", OriginalMasked: "联系 [PHONE_NUMBER]"}},
	}); err != nil {
		t.Fatal(err)
	}
	logs := store.List(Query{Limit: 10})
	if len(logs) != 1 {
		t.Fatalf("content toggle must not disable metadata logging, got %#v", logs)
	}
	if len(logs[0].Fields) != 0 {
		t.Fatalf("field content must not be stored when disabled: %#v", logs[0].Fields)
	}
	if logs[0].EntityCount != 2 {
		t.Fatalf("aggregate entity count must survive content toggle: %#v", logs[0])
	}
	stats := store.Stats(7)
	if stats.Requests != 1 || stats.Entities != 2 {
		t.Fatalf("stats must ignore content toggle: %#v", stats)
	}
}

func TestEntityCacheSettingsDefaultOnAndPersisted(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	if !settings.EntityCacheEnabled || settings.EntityCacheTTLSeconds != 900 {
		t.Fatalf("unexpected entity cache defaults: %#v", settings)
	}
	settings.EntityCacheEnabled = false
	settings.EntityCacheTTLSeconds = 60
	settings.Debug = true
	if err := store.Configure(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Settings(); got.EntityCacheEnabled || got.EntityCacheTTLSeconds != 60 || !got.Debug {
		t.Fatalf("entity cache settings were not persisted: %#v", got)
	}
}

func TestStorePersistsCompleteAuditFieldContent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("原始内容-", 1000)
	redacted := strings.Repeat("<MASK_CONTENT:ABCD>", 1000)
	if err := store.Add(Entry{
		UpstreamID: "long-content",
		Fields: []Field{{
			Path: "/messages/0/content", OriginalMasked: original, Redacted: redacted,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	logs := store.List(Query{Limit: 1})
	if len(logs) != 1 || len(logs[0].Fields) != 1 {
		t.Fatalf("expected one persisted audit field: %#v", logs)
	}
	if logs[0].Fields[0].OriginalMasked != original {
		t.Fatalf("original masked content was changed: got %d runes, want %d", len([]rune(logs[0].Fields[0].OriginalMasked)), len([]rune(original)))
	}
	if logs[0].Fields[0].Redacted != redacted {
		t.Fatalf("redacted content was changed: got %d bytes, want %d", len(logs[0].Fields[0].Redacted), len(redacted))
	}
}

func TestStoreReturnsFieldsInStablePathOrder(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Entry{
		UpstreamID: "test",
		Fields: []Field{
			{Path: "/messages/10/content"},
			{Path: "/messages/2/content"},
			{Path: "/messages/1/content"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	logs := store.List(Query{Limit: 1})
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	want := []string{"/messages/1/content", "/messages/2/content", "/messages/10/content"}
	for index, path := range want {
		if logs[0].Fields[index].Path != path {
			t.Fatalf("field %d path = %q, want %q", index, logs[0].Fields[index].Path, path)
		}
	}
}

func TestStatsHandlesFractionalAverageLatency(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, duration := range []int64{10, 11} {
		if err := store.Add(Entry{Timestamp: time.Now().UTC(), StatusCode: 200, DurationMS: duration}); err != nil {
			t.Fatal(err)
		}
	}
	stats := store.Stats(7)
	if stats.Requests != 2 || stats.AverageLatency != 10 {
		t.Fatalf("unexpected fractional average result: %#v", stats)
	}
}

func TestStatsRangeUsesHourlyBucketsAndExcludesFutureTime(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("test-local", 8*60*60)
	current := time.Now().In(location)
	now := time.Date(current.Year(), current.Month(), current.Day(), 12, 30, 0, 0, location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	entries := []Entry{
		{Timestamp: today.Add(time.Hour + 15*time.Minute), StatusCode: 200, EntityCount: 2},
		{Timestamp: today.Add(12*time.Hour + 15*time.Minute), StatusCode: 200, EntityCount: 3},
		{Timestamp: today.Add(13*time.Hour + 15*time.Minute), StatusCode: 200, EntityCount: 50},
		{Timestamp: today.AddDate(0, 0, -1).Add(23 * time.Hour), StatusCode: 200, EntityCount: 4},
	}
	for _, entry := range entries {
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	todayStats := store.statsForRange("today", now)
	if todayStats.Granularity != "hour" || len(todayStats.Daily) != 24 {
		t.Fatalf("unexpected today series: %#v", todayStats)
	}
	if todayStats.Requests != 2 || todayStats.Entities != 5 {
		t.Fatalf("today stats must exclude future and yesterday entries: %#v", todayStats)
	}
	if todayStats.Daily[1].Requests != 1 || todayStats.Daily[12].Entities != 3 {
		t.Fatalf("requests were not placed in local hourly buckets: %#v", todayStats.Daily)
	}
	if todayStats.Daily[13].Requests != 0 || todayStats.Daily[13].Entities != 0 {
		t.Fatalf("future hourly buckets must not include future audit data: %#v", todayStats.Daily[13])
	}

	yesterdayStats := store.statsForRange("yesterday", now)
	if yesterdayStats.Granularity != "hour" || len(yesterdayStats.Daily) != 24 {
		t.Fatalf("unexpected yesterday series: %#v", yesterdayStats)
	}
	if yesterdayStats.Requests != 1 || yesterdayStats.Daily[23].Entities != 4 {
		t.Fatalf("unexpected yesterday aggregation: %#v", yesterdayStats)
	}

	monthStats := store.statsForRange("30d", now)
	if monthStats.Granularity != "day" || len(monthStats.Daily) != 30 {
		t.Fatalf("unexpected 30-day series: %#v", monthStats)
	}
}
