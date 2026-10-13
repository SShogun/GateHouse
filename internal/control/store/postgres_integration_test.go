package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

func TestPostgresPublicationIsAtomicAndOptimistic(t *testing.T) {
	rawURL := os.Getenv("GATEHOUSE_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set GATEHOUSE_TEST_DATABASE_URL and GATEHOUSE_TEST_DB_CONFIRM=gatehouse_test to run PostgreSQL integration tests")
	}
	if os.Getenv("GATEHOUSE_TEST_DB_CONFIRM") != "gatehouse_test" {
		t.Fatal("refusing PostgreSQL integration test without GATEHOUSE_TEST_DB_CONFIRM=gatehouse_test")
	}
	adminConfig, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if adminConfig.ConnConfig.Database != "gatehouse_test" {
		t.Fatalf("refusing database %q; integration tests require database gatehouse_test", adminConfig.ConnConfig.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	if err = adminPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gatehouse_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err = adminPool.Exec(ctx, "CREATE SCHEMA "+(pgx.Identifier{schema}).Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		if _, dropErr := adminPool.Exec(dropCtx, "DROP SCHEMA "+(pgx.Identifier{schema}).Sanitize()+" CASCADE"); dropErr != nil {
			t.Errorf("drop isolated test schema: %v", dropErr)
		}
	}()
	isolatedConfig := adminConfig.Copy()
	isolatedConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, isolatedConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewPostgres(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx); err != nil {
		t.Fatalf("initial migration: %v", err)
	}
	if err = store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	if err = store.UpsertInstance(ctx, revision.Instance{ID: "dp-a", BootID: "boot-a", Build: "build-a", Connected: true, ActiveRevision: 1, ActiveHash: strings.Repeat("a", 64), LastNACK: "old"}); err != nil {
		t.Fatalf("insert instance status: %v", err)
	}
	if err = store.UpsertInstance(ctx, revision.Instance{ID: "dp-a", BootID: "boot-b", Build: "build-b", Connected: false, ActiveRevision: 2, ActiveHash: strings.Repeat("b", 64)}); err != nil {
		t.Fatalf("replace current boot status: %v", err)
	}
	instances, err := store.Instances(ctx, "", 10)
	if err != nil {
		t.Fatalf("list instance status: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "dp-a" || instances[0].BootID != "boot-b" || instances[0].Build != "build-b" || instances[0].Connected || instances[0].ActiveRevision != 2 || instances[0].ActiveHash != strings.Repeat("b", 64) || instances[0].LastNACK != "" || instances[0].LastHeartbeat.IsZero() {
		t.Fatalf("upsert did not retain exactly the current boot row: %#v", instances)
	}
	if err = store.UpsertInstance(ctx, revision.Instance{ID: "", BootID: "boot-c"}); err == nil {
		t.Fatal("empty instance ID was persisted")
	}
	if _, err = store.Active(ctx); !errors.Is(err, revision.ErrNotFound) {
		t.Fatalf("Active() before first publication error=%v, want ErrNotFound", err)
	}
	invalid := integrationCandidate("invalid")
	invalid.Routes[0].ClusterId = "missing-cluster"
	if _, err = store.Publish(ctx, revision.Publish{ExpectedParent: 0, Hash: strings.Repeat("0", 64), Config: invalid, Actor: "integration", Operation: "publish"}); err == nil {
		t.Fatal("invalid publication unexpectedly succeeded")
	}
	if revisions, listErr := store.List(ctx, 0, 10); listErr != nil || len(revisions) != 0 {
		t.Fatalf("invalid publication changed revision history: revisions=%d err=%v", len(revisions), listErr)
	}

	candidate := integrationCandidate("one")
	_, hash, err := config.EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan revision.Record, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := store.Publish(ctx, revision.Publish{ExpectedParent: 0, Hash: hash, Config: candidate, Actor: "integration", Reason: "first publish", Operation: "publish"})
			if e != nil {
				errs <- e
				return
			}
			first <- r
		}()
	}
	wg.Wait()
	close(first)
	close(errs)
	var created []revision.Record
	var conflicts int
	for r := range first {
		created = append(created, r)
	}
	for e := range errs {
		if errors.Is(e, revision.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent publish error=%v", e)
		}
	}
	if len(created) != 1 || conflicts != 1 {
		t.Fatalf("first publication results: created=%d conflicts=%d", len(created), conflicts)
	}
	r1 := created[0]
	if r1.Number <= 0 || r1.Parent != 0 || r1.Hash != hash {
		t.Fatalf("unexpected first revision: %#v", r1)
	}
	active, err := store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.Number != r1.Number || active.Hash != hash {
		t.Fatalf("active revision=%d hash=%s, want %d %s", active.Number, active.Hash, r1.Number, hash)
	}

	secondConfig := integrationCandidate("two")
	_, hash2, err := config.EncodeCandidate(secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.Publish(ctx, revision.Publish{ExpectedParent: r1.Number, Hash: hash2, Config: secondConfig, Actor: "integration", Reason: "second publish", Operation: "publish"})
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := store.Publish(ctx, revision.Publish{ExpectedParent: r2.Number, Hash: r1.Hash, Config: r1.Config, Actor: "integration", Reason: "rollback", Operation: "rollback", RollbackSource: r1.Number})
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Number <= r2.Number || rollback.Parent != r2.Number || rollback.RollbackSource != r1.Number || rollback.Operation != "rollback" || !proto.Equal(rollback.Config, r1.Config) {
		t.Fatalf("unexpected rollback revision: %#v", rollback)
	}
	history, err := store.List(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history count=%d, want three immutable revisions", len(history))
	}
	auditCount := int64(0)
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM config_audit").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("audit count=%d, want 3", auditCount)
	}

	// Fail after the revision and active-pointer writes. The transaction must roll back both.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TRIGGER reject_audit_insert BEFORE INSERT ON config_audit FOR EACH ROW EXECUTE FUNCTION reject_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err = store.Publish(ctx, revision.Publish{ExpectedParent: rollback.Number, Hash: hash2, Config: secondConfig, Actor: "integration", Reason: "must roll back", Operation: "publish"})
	if err == nil {
		t.Fatal("publication unexpectedly succeeded after audit insert failure")
	}
	after, err := store.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Number != rollback.Number || after.Hash != rollback.Hash {
		t.Fatalf("failed publication changed active revision: %#v", after)
	}
	history, err = store.List(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("failed transaction left revision behind: history count=%d", len(history))
	}
	auditCount = 0
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM config_audit").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("failed transaction left audit row: count=%d", auditCount)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER reject_audit_insert ON config_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "DROP FUNCTION reject_audit()"); err != nil {
		t.Fatal(err)
	}

	// A client pointed at an unavailable endpoint cannot alter the durable
	// active revision. Restoring the valid connection still allows publication.
	offlineConfig := isolatedConfig.Copy()
	offlineConfig.ConnConfig.Host = "127.0.0.1"
	offlineConfig.ConnConfig.Port = 1
	offlineConfig.MaxConnIdleTime = 100 * time.Millisecond
	offlinePool, poolErr := pgxpool.NewWithConfig(ctx, offlineConfig)
	if poolErr != nil {
		t.Fatal(poolErr)
	}
	offlineStore, storeErr := NewPostgres(offlinePool)
	if storeErr != nil {
		offlinePool.Close()
		t.Fatal(storeErr)
	}
	if _, unavailableErr := offlineStore.Active(ctx); unavailableErr == nil {
		offlinePool.Close()
		t.Fatal("unavailable PostgreSQL endpoint unexpectedly returned active config")
	}
	offlinePool.Close()
	stillActive, err := store.Active(ctx)
	if err != nil || stillActive.Number != rollback.Number || stillActive.Hash != rollback.Hash {
		t.Fatalf("unavailable endpoint changed durable active revision: active=%+v err=%v", stillActive, err)
	}
	recovered, err := store.Publish(ctx, revision.Publish{ExpectedParent: rollback.Number, Hash: hash2, Config: secondConfig, Actor: "integration", Reason: "after database recovery", Operation: "publish"})
	if err != nil {
		t.Fatalf("publication after PostgreSQL recovery: %v", err)
	}
	if recovered.Number <= rollback.Number || recovered.Parent != rollback.Number {
		t.Fatalf("recovery publication did not extend durable history: %#v", recovered)
	}
}

func integrationCandidate(endpoint string) *pb.GatewayConfig {
	return &pb.GatewayConfig{SchemaVersion: 1, Listener: &pb.Listener{Address: "127.0.0.1:8080"}, Clusters: []*pb.Cluster{{Id: "backend", Policy: pb.SelectionPolicy_SELECTION_POLICY_ROUND_ROBIN, Endpoints: []*pb.Endpoint{{Id: endpoint, Address: "http://127.0.0.1:9000", Weight: 1}}}}, Routes: []*pb.Route{{Id: "root", Path: "/", PathType: pb.PathType_PATH_TYPE_PREFIX, ClusterId: "backend"}}}
}
