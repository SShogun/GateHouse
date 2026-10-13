package store

import (
	"context"
	"testing"

	"github.com/SShogun/GateHouse/internal/control/revision"
)

func TestUpsertInstanceRequiresIdentity(t *testing.T) {
	for name, instance := range map[string]revision.Instance{
		"instance ID": {BootID: "boot-a"},
		"boot ID":     {ID: "dp-a"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (&Postgres{}).UpsertInstance(context.Background(), instance); err == nil {
				t.Fatalf("UpsertInstance accepted an empty %s", name)
			}
		})
	}
}
