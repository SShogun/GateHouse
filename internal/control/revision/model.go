// Package revision defines durable configuration revisions and publication semantics.
package revision

import (
	"context"
	"errors"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
)

var (
	ErrNotFound = errors.New("revision not found")
	ErrConflict = errors.New("active revision changed")
	ErrCorrupt  = errors.New("stored revision integrity failure")
)

type Record struct {
	Number         int64
	Parent         int64
	Hash           string
	Config         *pb.GatewayConfig
	CreatedAt      time.Time
	Actor          string
	Reason         string
	Operation      string
	RollbackSource int64
}

type Publish struct {
	ExpectedParent int64
	Hash           string
	Config         *pb.GatewayConfig
	Actor          string
	Reason         string
	Operation      string
	RollbackSource int64
}

type Instance struct {
	ID             string
	BootID         string
	Build          string
	Connected      bool
	ActiveRevision int64
	ActiveHash     string
	LastHeartbeat  time.Time
	LastNACK       string
}

type Store interface {
	Active(context.Context) (Record, error)
	Get(context.Context, int64) (Record, error)
	List(context.Context, int64, int) ([]Record, error)
	Instances(context.Context, string, int) ([]Instance, error)
	Publish(context.Context, Publish) (Record, error)
}

// InstanceStore persists the latest process status reported over a data-plane stream.
type InstanceStore interface {
	UpsertInstance(context.Context, Instance) error
}
