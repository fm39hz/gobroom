package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/fm39hz/gobroom/internal/extensions"
)

func TestUsageHistoryRejectsIncompleteRecordedSchemaClosure(t *testing.T) {
	for _, mutation := range []string{
		"missing_nonroot_link",
		"changed_nonroot_link_digest",
		"changed_dependency_document",
		"rewritten_dependency_document_and_digests",
		"missing_dependency_document",
	} {
		t.Run(mutation, func(t *testing.T) {
			path := t.TempDir() + "/dependency-history.db"
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			catalog, dependencyRef := usageHistoryDependencyCatalog(t)
			if err := s.SetUsageCatalog(catalog); err != nil {
				t.Fatal(err)
			}
			const value = `9007199254740993.125`
			if err := s.SaveUsageEvent(context.Background(), usageHistoryEvent(value)); err != nil {
				t.Fatal(err)
			}
			events, err := s.UsageEvents(1)
			if err != nil || len(events) != 1 {
				t.Fatalf("saved history: events=%d err=%v", len(events), err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s = reopened
			// No catalog is bound after restart. Both intact reads and corruption
			// detection must use only the archived data contract and schemas.
			detail, err := s.UsageDetail(context.Background(), events[0].ID)
			if err != nil || len(detail.Extensions) != 1 || string(detail.Extensions[0].Value) != value {
				t.Fatalf("intact dependency archive: detail=%#v err=%v", detail, err)
			}

			linkArgs := []any{usageHistoryRef.Kind, usageHistoryRef.ID, usageHistoryRef.ContractVersion,
				dependencyRef.Kind, dependencyRef.ID, dependencyRef.ContractVersion}
			const linkWhere = ` WHERE descriptor_kind=? AND descriptor_id=? AND descriptor_version=? AND schema_kind=? AND schema_id=? AND schema_version=? AND is_root=0`
			schemaArgs := []any{dependencyRef.Kind, dependencyRef.ID, dependencyRef.ContractVersion}
			const schemaWhere = ` WHERE kind=? AND id=? AND contract_version=?`
			switch mutation {
			case "missing_nonroot_link":
				mutateUsageArchiveRow(t, s, `DELETE FROM usage_extension_descriptor_schemas`+linkWhere, linkArgs...)
			case "changed_nonroot_link_digest":
				args := append([]any{"changed-dependency-digest"}, linkArgs...)
				mutateUsageArchiveRow(t, s, `UPDATE usage_extension_descriptor_schemas SET schema_digest=?`+linkWhere, args...)
			case "changed_dependency_document":
				args := append([]any{[]byte(`{"type":"string"}`)}, schemaArgs...)
				mutateUsageArchiveRow(t, s, `UPDATE usage_extension_schemas SET document=?`+schemaWhere, args...)
			case "rewritten_dependency_document_and_digests":
				// All mutable dependency rows agree with each other after this
				// rewrite. The original immutable closure must still reject it.
				document, err := extensions.BindSchemaDocument(dependencyRef, json.RawMessage(`{"type":"string"}`))
				if err != nil {
					t.Fatal(err)
				}
				digestBytes := sha256.Sum256(document)
				digest := hex.EncodeToString(digestBytes[:])
				args := append([]any{[]byte(document), digest}, schemaArgs...)
				mutateUsageArchiveRow(t, s, `UPDATE usage_extension_schemas SET document=?,digest=?`+schemaWhere, args...)
				args = append([]any{digest}, linkArgs...)
				mutateUsageArchiveRow(t, s, `UPDATE usage_extension_descriptor_schemas SET schema_digest=?`+linkWhere, args...)
			case "missing_dependency_document":
				// Remove the link first so the fixture respects foreign keys.
				mutateUsageArchiveRow(t, s, `DELETE FROM usage_extension_descriptor_schemas`+linkWhere, linkArgs...)
				mutateUsageArchiveRow(t, s, `DELETE FROM usage_extension_schemas`+schemaWhere, schemaArgs...)
			}
			if _, err := s.UsageDetail(context.Background(), events[0].ID); err == nil {
				t.Fatal("historical detail accepted an incomplete or altered recorded schema closure")
			}
		})
	}
}

func usageHistoryDependencyCatalog(t *testing.T) (*extensions.Snapshot, extensions.Ref) {
	t.Helper()
	catalog := extensions.NewCatalog()
	dependencyRef := extensions.Ref{Kind: extensions.SchemaKind, ID: "vendor.example.precise.number", ContractVersion: 1}
	dependencyURI, err := dependencyRef.URI()
	if err != nil {
		t.Fatal(err)
	}
	rootBody, err := json.Marshal(map[string]string{"$ref": dependencyURI})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []struct {
		ref  extensions.Ref
		body json.RawMessage
	}{
		{ref: dependencyRef, body: json.RawMessage(`{"type":"number"}`)},
		{ref: usageHistorySchemaRef, body: rootBody},
	} {
		document, err := extensions.BindSchemaDocument(schema.ref, schema.body)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalog.RegisterSchema(schema.ref, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.RegisterDescriptor(extensions.Descriptor{
		Ref: usageHistoryRef, ImplementationVersion: "1", DisplayName: "Precise measurement",
		Description: "Measurement with an archived schema dependency.", InputSchemaRef: &usageHistorySchemaRef,
		UsagePolicy: &extensions.UsagePolicy{Unit: "items", Disclosure: extensions.UsagePrivate, MaxValueBytes: 1024},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := snapshot.LockDependencies([]extensions.Ref{usageHistorySchemaRef})
	if err != nil || len(lock.Schemas) != 2 {
		t.Fatalf("fixture schema closure: pins=%#v err=%v", lock.Schemas, err)
	}
	return snapshot, dependencyRef
}

func mutateUsageArchiveRow(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	result, err := s.DB.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		t.Fatalf("archive mutation affected %d rows, want 1: %v", changed, err)
	}
}
