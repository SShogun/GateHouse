// Package store persists immutable config revisions in PostgreSQL.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationVersion int64 = 1

// Postgres implements revision.Store. Call Migrate explicitly during process startup.
type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) (*Postgres, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &Postgres{pool: pool}, nil
}

func (s *Postgres) Migrate(ctx context.Context) error {
	sql, err := migrationFiles.ReadFile("migrations/001_initial.sql")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(sql)
	checksum := hex.EncodeToString(sum[:])
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x47415445484f5553)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	// This bootstrap table is the migration registry and precedes numbered migrations.
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version BIGINT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
 )`); err != nil {
		return fmt.Errorf("create migration registry: %w", err)
	}
	var applied string
	err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", migrationVersion).Scan(&applied)
	if err == nil {
		if applied != checksum {
			return fmt.Errorf("migration %d checksum mismatch", migrationVersion)
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read migration registry: %w", err)
	}
	if _, err = tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", migrationVersion, checksum); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (s *Postgres) Active(ctx context.Context) (revision.Record, error) {
	r, err := scanRevision(s.pool.QueryRow(ctx, `SELECT r.revision,COALESCE(r.parent_revision,0),r.content_hash,r.config,r.created_at,r.actor,r.reason,r.operation,COALESCE(r.rollback_source_revision,0)
 FROM active_config a JOIN config_revisions r USING(revision) WHERE a.singleton=true`))
	if errors.Is(err, pgx.ErrNoRows) {
		return revision.Record{}, revision.ErrNotFound
	}
	return r, err
}
func (s *Postgres) Get(ctx context.Context, n int64) (revision.Record, error) {
	r, err := scanRevision(s.pool.QueryRow(ctx, `SELECT revision,COALESCE(parent_revision,0),content_hash,config,created_at,actor,reason,operation,COALESCE(rollback_source_revision,0) FROM config_revisions WHERE revision=$1`, n))
	if errors.Is(err, pgx.ErrNoRows) {
		return revision.Record{}, revision.ErrNotFound
	}
	return r, err
}
func (s *Postgres) List(ctx context.Context, before int64, limit int) ([]revision.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT revision,COALESCE(parent_revision,0),content_hash,NULL::bytea,created_at,actor,reason,operation,COALESCE(rollback_source_revision,0) FROM config_revisions WHERE ($1=0 OR revision<$1) ORDER BY revision DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []revision.Record
	for rows.Next() {
		r, e := scanRevision(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *Postgres) Instances(ctx context.Context, before string, limit int) ([]revision.Instance, error) {
	rows, err := s.pool.Query(ctx, `SELECT instance_id,boot_id,build,connected,active_revision,active_hash,last_heartbeat,last_nack FROM data_plane_instances WHERE ($1='' OR instance_id>$1) ORDER BY instance_id LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []revision.Instance
	for rows.Next() {
		var instance revision.Instance
		if err := rows.Scan(&instance.ID, &instance.BootID, &instance.Build, &instance.Connected, &instance.ActiveRevision, &instance.ActiveHash, &instance.LastHeartbeat, &instance.LastNACK); err != nil {
			return nil, err
		}
		result = append(result, instance)
	}
	return result, rows.Err()
}

// UpsertInstance stores the current boot row for a data-plane instance. A new
// boot ID replaces the previous process state for the same stable instance ID.
func (s *Postgres) UpsertInstance(ctx context.Context, instance revision.Instance) error {
	if strings.TrimSpace(instance.ID) == "" {
		return errors.New("instance ID is required")
	}
	if strings.TrimSpace(instance.BootID) == "" {
		return errors.New("boot ID is required")
	}
	var heartbeat any
	if !instance.LastHeartbeat.IsZero() {
		heartbeat = instance.LastHeartbeat
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO data_plane_instances(instance_id,boot_id,build,connected,active_revision,active_hash,last_heartbeat,last_nack)
 VALUES($1,$2,$3,$4,$5,$6,COALESCE($7,now()),$8)
 ON CONFLICT(instance_id) DO UPDATE SET boot_id=EXCLUDED.boot_id,build=EXCLUDED.build,connected=EXCLUDED.connected,
 active_revision=EXCLUDED.active_revision,active_hash=EXCLUDED.active_hash,last_heartbeat=EXCLUDED.last_heartbeat,last_nack=EXCLUDED.last_nack`,
		instance.ID, instance.BootID, instance.Build, instance.Connected, instance.ActiveRevision, instance.ActiveHash, heartbeat, instance.LastNACK)
	return err
}

func (s *Postgres) Publish(ctx context.Context, p revision.Publish) (revision.Record, error) {
	if p.Config == nil || p.Hash == "" {
		return revision.Record{}, errors.New("validated config and hash are required")
	}
	data, hash, err := config.EncodeCandidate(p.Config)
	if err != nil {
		return revision.Record{}, fmt.Errorf("validate config: %w", err)
	}
	if hash != p.Hash {
		return revision.Record{}, revision.ErrCorrupt
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return revision.Record{}, fmt.Errorf("begin publication: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x47415445484f5553)); err != nil {
		return revision.Record{}, fmt.Errorf("lock publication: %w", err)
	}
	var parent int64
	err = tx.QueryRow(ctx, "SELECT revision FROM active_config WHERE singleton=true FOR UPDATE").Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		parent = 0
	} else if err != nil {
		return revision.Record{}, fmt.Errorf("read active revision: %w", err)
	}
	if parent != p.ExpectedParent {
		return revision.Record{}, revision.ErrConflict
	}
	if (p.Operation != "publish" && p.Operation != "rollback") || p.Actor == "" {
		return revision.Record{}, errors.New("operation and actor are required")
	}
	var rollback any
	if p.Operation == "rollback" {
		if p.RollbackSource <= 0 {
			return revision.Record{}, errors.New("rollback source is required")
		}
		rollback = p.RollbackSource
	} else if p.RollbackSource != 0 {
		return revision.Record{}, errors.New("publish cannot specify rollback source")
	}
	var r revision.Record
	r.Parent = parent
	r.Hash = hash
	r.Config = proto.Clone(p.Config).(*pb.GatewayConfig)
	r.Actor = p.Actor
	r.Reason = p.Reason
	r.Operation = p.Operation
	r.RollbackSource = p.RollbackSource
	var parentValue any
	if parent > 0 {
		parentValue = parent
	}
	err = tx.QueryRow(ctx, `INSERT INTO config_revisions(parent_revision,content_hash,config,actor,reason,operation,rollback_source_revision)
 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING revision,created_at`, parentValue, hash, data, p.Actor, p.Reason, p.Operation, rollback).Scan(&r.Number, &r.CreatedAt)
	if err != nil {
		return revision.Record{}, fmt.Errorf("insert revision: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO active_config(singleton,revision,content_hash) VALUES(true,$1,$2)
 ON CONFLICT(singleton) DO UPDATE SET revision=EXCLUDED.revision,content_hash=EXCLUDED.content_hash`, r.Number, hash)
	if err != nil {
		return revision.Record{}, fmt.Errorf("update active revision: %w", err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO config_audit(revision,operation,actor,reason) VALUES($1,$2,$3,$4)", r.Number, p.Operation, p.Actor, p.Reason); err != nil {
		return revision.Record{}, fmt.Errorf("insert audit entry: %w", err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_notify('gatehouse_config', $1)", fmt.Sprintf("%d:%s", r.Number, r.Hash)); err != nil {
		return revision.Record{}, fmt.Errorf("notify config subscribers: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return revision.Record{}, fmt.Errorf("commit publication: %w", err)
	}
	return r, nil
}

type rowScanner interface{ Scan(...any) error }

func scanRevision(row rowScanner) (revision.Record, error) {
	var r revision.Record
	var data []byte
	err := row.Scan(&r.Number, &r.Parent, &r.Hash, &data, &r.CreatedAt, &r.Actor, &r.Reason, &r.Operation, &r.RollbackSource)
	if err != nil {
		return revision.Record{}, err
	}
	if data != nil {
		p := new(pb.GatewayConfig)
		if err = proto.Unmarshal(data, p); err != nil {
			return revision.Record{}, fmt.Errorf("decode revision %d: %w", r.Number, revision.ErrCorrupt)
		}
		_, hash, e := config.EncodeCandidate(p)
		if e != nil || hash != r.Hash {
			return revision.Record{}, fmt.Errorf("revision %d hash mismatch: %w", r.Number, revision.ErrCorrupt)
		}
		r.Config = p
	}
	return r, nil
}

var _ revision.Store = (*Postgres)(nil)
