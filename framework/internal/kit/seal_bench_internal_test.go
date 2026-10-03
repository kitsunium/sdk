package kit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
)

// What sealing costs a store's writes and reads (ADR 0006 §4). Both engines
// keep their files in memory, so the durable write a store on disk pays —
// about 11 ms on the measured Mac, fsync — is left out, and what is left is
// kit's: the JSON walk, one seal per member, the key's check. The data keys
// are kit's own store's, on disk, and cached once opened.
//
//	go test -run '^$' -bench Sealing -benchmem ./kit/

// benchPatient is a record with five members kit seals and three it does
// not.
type benchPatient struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Name  string `json:"name" kit:"personal"`
	Phone string `json:"phone" kit:"personal"`
	Notes string `json:"notes" kit:"personal"`
	Token string `json:"token" kit:"secret"`
	Ward  string `json:"ward" kit:"public"`
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
func benchEngines(b *testing.B) (plain storeEngine[benchPatient], sealed storeEngine[benchPatient]) {
	b.Helper()
	pinDataKeyWithoutFileStore(b)
	svc := NewService("bench", "Records a benchmark seals.")
	st := svc.Store("patients", func(p benchPatient) string { return p.ID })
	app := NewApp("bench", svc).With(DataDir(b.TempDir()), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	stopAtCleanup(b, app)
	p, err := openDocEngine(st.key, storeFS{fs: vfs.NewMem(), path: "bench/plain.json"}, nil, versionsOn{})
	if err != nil {
		b.Fatal(err)
	}
	z, err := app.sealing(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	s, err := openSealedEngine(st, app, z, nil, func(key func(sealedDoc) string, specs []docstore.IndexSpec[sealedDoc]) (storeEngine[sealedDoc], error) {
		return openDocEngine(key, storeFS{fs: vfs.NewMem(), path: "bench/sealed.json"}, specs, versionsOn{})
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := errors.Join(p.Close(), s.Close()); err != nil {
			b.Errorf("close: %v", err)
		}
	})
	return p, s
}

func BenchmarkSealing(b *testing.B) {
	plain, sealed := benchEngines(b)
	ctx := context.Background()
	for _, e := range []struct {
		name string
		eng  storeEngine[benchPatient]
	}{{"plain", plain}, {"sealed", sealed}} {
		// A hundred people, a key each, opened once before the clock runs.
		for i := range 100 {
			if err := e.eng.Write(ctx, benchRecord(i), upsert); err != nil {
				b.Fatal(err)
			}
		}
		b.Run("write/"+e.name, func(b *testing.B) {
			i := 0
			for b.Loop() {
				if err := e.eng.Write(ctx, benchRecord(i%1000), upsert); err != nil {
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
