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
		OperationID: "create-chat-completion", Method: "POST", Path: "/v1/chat/completions",
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
	stats := loaded.Stats(7)
	if stats.Requests != 1 || stats.Entities != 1 || stats.EntityTypes["PHONE_NUMBER"] != 1 || stats.SuccessRate != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
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
	if !settings.EntityCacheEnabled || settings.EntityCacheTTLSeconds != 300 {
		t.Fatalf("unexpected entity cache defaults: %#v", settings)
	}
	settings.EntityCacheEnabled = false
	settings.EntityCacheTTLSeconds = 60
	if err := store.Configure(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Settings(); got.EntityCacheEnabled || got.EntityCacheTTLSeconds != 60 {
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
		Fields:     []Field{{Path: "/placeholder"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a row written by an older Core version, before field ordering was
	// normalized on write.
	if _, err := store.db.Exec(`UPDATE audit_entries SET fields_json = ?`, `[
		{"path":"/messages/10/content"},
		{"path":"/messages/2/content"},
		{"path":"/messages/1/content"}
	]`); err != nil {
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
