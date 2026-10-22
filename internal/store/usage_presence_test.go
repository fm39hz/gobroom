package store

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
)

func TestUsagePresenceRoundTripAndDailyCoverage(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-presence.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	date := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	zero := new(int64(0))
	seven := new(int64(7))
	five := new(int64(5))
	price := new(float64(0))
	issue := kernel.UsageIssue{Code: "invalid_counter", TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: "fixture", ContractVersion: 1}}
	if err := s.SaveUsageEvent(context.Background(), kernel.UsageEvent{
		At: date, InputTokens: zero, OutputTokens: seven, EstimatedCost: price,
		CachedReadTokens: zero, CachedWriteTokens: five,
		Latency: 500 * time.Microsecond, FirstByteAt: date.Add(time.Second),
		AccountingIssues: []kernel.UsageIssue{issue},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUsageEvent(context.Background(), kernel.UsageEvent{At: date.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	events, err := s.UsageEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d usage events, want 2", len(events))
	}
	known := events[1]
	if known.InputTokens == nil || *known.InputTokens != 0 || known.OutputTokens == nil || *known.OutputTokens != 7 {
		t.Fatalf("known zero or nonzero count lost: %#v", known)
	}
	if known.EstimatedCost == nil || *known.EstimatedCost != 0 {
		t.Fatalf("explicit zero cost lost: %#v", known.EstimatedCost)
	}
	if known.CachedReadTokens == nil || *known.CachedReadTokens != 0 || known.CachedWriteTokens == nil || *known.CachedWriteTokens != 5 || known.ReasoningTokens != nil {
		t.Fatalf("cached/reasoning presence lost: read=%v write=%v reasoning=%v", known.CachedReadTokens, known.CachedWriteTokens, known.ReasoningTokens)
	}
	if known.LatencyMS == nil || *known.LatencyMS != 0 || known.TTFTMS == nil || *known.TTFTMS != 0 {
		t.Fatalf("known submillisecond latency/first byte became unknown: latency=%v ttft=%v", known.LatencyMS, known.TTFTMS)
	}
	if len(known.AccountingIssues) != 1 || known.AccountingIssues[0] != issue {
		t.Fatalf("accounting issue was not safely persisted: %#v", known.AccountingIssues)
	}
	unknown := events[0]
	if unknown.InputTokens != nil || unknown.OutputTokens != nil || unknown.EstimatedCost != nil || unknown.LatencyMS != nil || unknown.TTFTMS != nil {
		t.Fatalf("unknown usage became reported: %#v", unknown)
	}

	days, err := s.UsageSummary(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("got %d daily rows, want 1", len(days))
	}
	daily := days[0]
	if daily.Attempts != 2 || daily.IncompleteAttempts != 1 || daily.Input.Reported != 1 || daily.Input.Sum != 0 || daily.Output.Reported != 1 || daily.Output.Sum != 7 {
		t.Fatalf("daily token coverage incorrect: %#v", daily)
	}
	if daily.CachedRead.Reported != 1 || daily.CachedRead.Sum != 0 || daily.CachedWrite.Reported != 1 || daily.CachedWrite.Sum != 5 || daily.Reasoning.Reported != 0 {
		t.Fatalf("daily cached/reasoning coverage incorrect: %#v", daily)
	}
	if daily.EstimatedCost.Reported != 1 || daily.EstimatedCost.Sum != 0 {
		t.Fatalf("daily cost coverage incorrect: %#v", daily.EstimatedCost)
	}
}

func TestUsageSchemaResetPreservesConfiguration(t *testing.T) {
	path := t.TempDir() + "/legacy-usage.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.runtime.close(); err != nil {
		t.Fatal(err)
	}
	s.runtime = runtimeStatements{}
	_, err = s.DB.Exec(`
DROP TABLE usage_events;
DROP TABLE usage_daily;
CREATE TABLE usage_events (id INTEGER PRIMARY KEY, timestamp TEXT NOT NULL, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0);
CREATE TABLE usage_daily (date_key TEXT PRIMARY KEY, requests INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0, estimated_cost REAL NOT NULL DEFAULT 0);
INSERT INTO usage_events(timestamp,input_tokens,output_tokens) VALUES('2026-10-20T00:00:00Z',0,4);
INSERT INTO usage_daily(date_key,requests,prompt_tokens,completion_tokens,estimated_cost) VALUES('2026-10-20',1,0,4,0);
CREATE TABLE unrelated_config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO unrelated_config VALUES('provider','{"id":"keep-me","enabled":true}');
INSERT INTO provider_nodes(id,name,base_url,protocol,definition_id,definition_contract_version,config_json) VALUES('node-preserve','Preserve','https://preserve.test','openai_chat','openai-compatible-chat',1,'{"token":"node-config"}');
INSERT INTO connections(id,provider_node_id,name,credential_type,secret_ref) VALUES('conn-preserve','node-preserve','Preserve','api_key','secret://keep');
INSERT INTO model_catalog(id,provider_node_id,kind,external_id,display_name,overrides_json) VALUES('model-preserve','node-preserve','custom','model-x','Model X','{"temperature":0.25}');
INSERT INTO provider_runtime(provider_node_id,status) VALUES('node-preserve','cooldown');
INSERT INTO connection_runtime(connection_id,status) VALUES('conn-preserve','rate_limited');
`)
	if err != nil {
		t.Fatal(err)
	}
	var credentialRevisionBefore string
	if err := s.DB.QueryRow(`SELECT revision FROM connection_credential_revisions WHERE connection_id='conn-preserve'`).Scan(&credentialRevisionBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareRuntimeStatements(); err != nil {
		t.Fatal(err)
	}
	var config string
	if err := s.DB.QueryRow(`SELECT value FROM unrelated_config WHERE key='provider'`).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if config != `{"id":"keep-me","enabled":true}` {
		t.Fatalf("unrelated configuration changed: %s", config)
	}
	var events, days int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if events != 0 || days != 0 {
		t.Fatalf("legacy ambiguous usage was retained: events=%d days=%d", events, days)
	}
	reset := s.UsageResetCounts()
	if reset.Events != 1 || reset.Daily != 1 {
		t.Fatalf("usage reset counts=%#v, want 1 event and 1 daily row", reset)
	}
	var nodeConfig, connectionSecretRef, modelConfig, providerHealth, connectionHealth, credentialRevision string
	if err := s.DB.QueryRow(`SELECT config_json FROM provider_nodes WHERE id='node-preserve'`).Scan(&nodeConfig); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT secret_ref FROM connections WHERE id='conn-preserve'`).Scan(&connectionSecretRef); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT overrides_json FROM model_catalog WHERE id='model-preserve'`).Scan(&modelConfig); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT status FROM provider_runtime WHERE provider_node_id='node-preserve'`).Scan(&providerHealth); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT status FROM connection_runtime WHERE connection_id='conn-preserve'`).Scan(&connectionHealth); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT revision FROM connection_credential_revisions WHERE connection_id='conn-preserve'`).Scan(&credentialRevision); err != nil {
		t.Fatal(err)
	}
	if nodeConfig != `{"token":"node-config"}` || connectionSecretRef != "secret://keep" || modelConfig != `{"temperature":0.25}` || providerHealth != "cooldown" || connectionHealth != "rate_limited" || credentialRevision != credentialRevisionBefore {
		t.Fatalf("configuration changed during usage reset: provider=%s connection=%s model=%s providerHealth=%s connectionHealth=%s credentialRevision=%s", nodeConfig, connectionSecretRef, modelConfig, providerHealth, connectionHealth, credentialRevision)
	}
}

func TestUsagePresenceAtomicRollbackAndCancellation(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-atomic.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxOpenConns(1)
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_daily BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(FAIL,'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	zero := new(int64(0))
	event := kernel.UsageEvent{At: time.Date(2026, time.October, 21, 0, 0, 0, 0, time.UTC), InputTokens: zero}
	if err := s.SaveUsageEvent(context.Background(), event); err == nil {
		t.Fatal("failed daily write was accepted")
	}
	var events, days int
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_daily`).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if events != 0 || days != 0 {
		t.Fatalf("event/daily transaction split: events=%d days=%d", events, days)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SaveUsageEvent(ctx, event); err == nil {
		t.Fatal("canceled write was accepted")
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("canceled write persisted event: count=%d err=%v", events, err)
	}
}

func TestUsageAccountingIssuePersistenceIsBounded(t *testing.T) {
	s, err := Open(t.TempDir() + "/usage-issues.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	issues := make([]kernel.UsageIssue, 40)
	for i := range issues {
		issues[i] = kernel.UsageIssue{Code: "invalid_counter", TypeRef: extensions.Ref{Kind: extensions.UsageKind, ID: string(rune('a' + i)), ContractVersion: 1}}
	}
	if err := s.SaveUsageEvent(context.Background(), kernel.UsageEvent{At: time.Now(), AccountingIssues: issues}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UsageEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].AccountingIssues) != 33 || rows[0].AccountingIssues[32].Code != "overflow" {
		t.Fatalf("accounting issue list was not bounded: %#v", rows)
	}
}
