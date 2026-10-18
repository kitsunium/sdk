package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"

	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
)

// What sealing costs a store's writes and reads (ADR 0006 §4). Both engines
// keep their files in memory, so the durable write a store on disk pays —
// about 11 ms on the measured Mac, fsync — is left out, and what is left is
// kit's: the JSON walk, one seal per member, the key's check. The data keys
// are kit's own store's, on disk, and cached once opened.
//
//	go test -run '^$' -bench Sealing -benchmem ./app/

// benchPatient is a record with five members kit seals and three it does
// not.
type benchPatient struct {
	ID    string `json:"id"`
	Email string `json:"email" app:"subject"`
	Name  string `json:"name" app:"personal"`
	Phone string `json:"phone" app:"personal"`
	Notes string `json:"notes" app:"personal"`
	Token string `json:"token" app:"secret"`
	Ward  string `json:"ward" app:"public"`
	Age   int    `json:"age"`
	Visit string `json:"visit"`
}

func benchRecord(i int) benchPatient {
	return benchPatient{
		ID: fmt.Sprintf("p%06d", i), Email: fmt.Sprintf("person%d@clinic.test", i%100),
		Name: "Annabel Quist", Phone: "+33 6 12 34 56 78", Notes: "complains of migraines since the spring",
		Token: "tok-9f86d081884c7d659a2feaa0c55ad015", Ward: "ward-east", Age: 41, Visit: "2026-09-28",
	}
}

// benchEngines runs a store of benchPatients in an app on disk, for its
// sealer, and returns an engine of each kind over files in memory.
//
// IFACE-OPAQUE: the benchmark measures the port, whichever engine is behind
// it.
func benchEngines(b *testing.B) (st *StoreService[benchPatient], plain, sealed plug.StoreEngine) {
	b.Helper()
	pinDataKeyWithoutFileStore(b, "bench")
	svc := NewService("bench", "Records a benchmark seals.")
	st = svc.Store("patients", func(p benchPatient) string { return p.ID })
	app := NewApp("bench", svc).With(DataDir(b.TempDir()), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	stopAtCleanup(b, app)
	p, err := openLocalEngine("bench/plain", st.engineConfig(nil), storeFS{fs: vfs.NewMem(), path: "bench/plain.json"}, versionsOn{})
	if err != nil {
		b.Fatal(err)
	}
	z, err := app.sealing(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	s, err := openSealedEngine(&st.storeCore, app, z, nil, func(cfg *engineConfig) (plug.StoreEngine, error) {
		return openLocalEngine("bench/sealed", cfg, storeFS{fs: vfs.NewMem(), path: "bench/sealed.json"}, versionsOn{})
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := errors.Join(p.Close(), s.Close()); err != nil {
			b.Errorf("close: %v", err)
		}
	})
	return st, p, s
}

// benchDoc is p as the store's engine takes it.
func benchDoc(b *testing.B, st *StoreService[benchPatient], p benchPatient) plug.DocumentValue {
	b.Helper()
	d, err := st.docOf(p)
	if err != nil {
		b.Fatal(err)
	}
	return d
}

func BenchmarkSealing(b *testing.B) {
	st, plain, sealed := benchEngines(b)
	ctx := context.Background()
	for _, e := range []struct {
		name string
		eng  plug.StoreEngine
	}{{"plain", plain}, {"sealed", sealed}} {
		// A hundred people, a key each, opened once before the clock runs.
		for i := range 100 {
			if err := e.eng.Write(ctx, benchDoc(b, st, benchRecord(i)), plug.WriteUpsert); err != nil {
				b.Fatal(err)
			}
		}
		b.Run("write/"+e.name, func(b *testing.B) {
			i := 0
			for b.Loop() {
				if err := e.eng.Write(ctx, benchDoc(b, st, benchRecord(i%1000)), plug.WriteUpsert); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
		b.Run("read/"+e.name, func(b *testing.B) {
			i := 0
			for b.Loop() {
				if _, err := e.eng.Get(ctx, benchRecord(i%100).ID); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
