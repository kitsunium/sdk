package kit_test

import (
	"context"

	kit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// sqlMigrations is kit.Migrations given the SDK's migrations, as
// framework/kit's facade hands them to the core (ADR 0179 Q5): the same
// versions, names and steps, on the standard library's executor.
func sqlMigrations(ms ...sql.Migration) interface {
	kit.DatabaseConfigurer
	kit.ModuleConfigurer
} {
	out := make([]plug.MigrationValue, len(ms))
	for i, m := range ms {
		out[i] = plug.MigrationValue{Version: m.Version, Name: m.Name, Up: sqlStep(m.Up), Down: sqlStep(m.Down)}
	}
	return kit.Migrations(out...)
}

// sqlStep is s on the standard library's executor, nil for none.
func sqlStep(s sql.Step) plug.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex plug.Executor) error { return s(ctx, ex) }
}
