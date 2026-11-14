package app

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The terminal's lines are redacted like the Studio's: a name that says
// secret, a URL's credentials, and the classified members of a value
// logged whole — which the SDK's encoders would write as "?".
func TestTheTerminalIsRedacted(t *testing.T) {
	var out bytes.Buffer
	sink, err := logger.NewWriterSink(&out)
	if err != nil {
		t.Fatal(err)
	}
	for _, enc := range []logger.Encoder{logger.NewJSONEncoder(), logger.NewTextEncoder()} {
		out.Reset()
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: redactingEncoder{enc}})
		if err != nil {
			t.Fatal(err)
		}
		logger.Info(t.Context(), lg, "dial postgres://ann:pw@db/x",
			logger.Any("who", redactedByClass{Name: "n1", Health: "h1", Category: "c1"}),
			logger.String("password", "hunter2"), logger.Int("count", 3))
		line := out.String()
		for _, leaked := range []string{"n1", "h1", "hunter2", "ann:pw"} {
			if strings.Contains(line, leaked) {
				t.Errorf("%s: %s leaked: %s", enc.Name(), leaked, line)
			}
		}
		for _, kept := range []string{"c1", "3", "[redacted]"} {
			if !strings.Contains(line, kept) {
				t.Errorf("%s: %s is not on the line: %s", enc.Name(), kept, line)
			}
		}
	}
}

// The terminal's encoder renders the bound attributes once, redacted when
// they are bound (kitsunium/sdk#1016): over each of the SDK's encoders it is
// a PrefixEncoder whose line is byte for byte the line the whole-record path
// writes — bound secrets, a URL's credentials and a value logged whole
// redacted under the groups in force — and a logger bound to them leaks none.
func TestTheTerminalRedactsBoundAttributesOnce(t *testing.T) {
	type tc struct {
		name   string
		enc    logger.Encoder
		groups []string
	}
	tests := []tc{
		{name: "json", enc: logger.NewJSONEncoder()},
		{name: "text", enc: logger.NewTextEncoder()},
		{name: "text under groups", enc: logger.NewTextEncoder(), groups: []string{"db", "pool"}},
	}
	at := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	bound := []logger.Attr{
		logger.String("password", "hunter2"),
		logger.String("dsn", "postgres://ann:pw@db/x"),
		logger.Any("who", redactedByClass{Name: "n1", Health: "h1", Category: "c1"}),
		logger.Int("count", 3),
	}
	own := []logger.Attr{logger.String("token", "t0k3n"), logger.String("route", "/orders")}
	runCase := func(t *testing.T, c tc) {
		enc, ok := redacting(c.enc).(redactingPrefixEncoder)
		if !ok {
			t.Fatalf("redacting(%s) is %T, want a redactingPrefixEncoder", c.name, redacting(c.enc))
		}
		whole := redactingEncoder{c.enc}.Append(nil, c.groups, logger.Record{
			Time: at, Level: logger.LevelInfo, Message: "dial postgres://bob:pw2@db/y", Attrs: append(slices.Clone(bound), own...),
		})
		spliced := enc.AppendWithPrefix(nil, c.groups, enc.AppendAttrs(nil, c.groups, bound), logger.Record{
			Time: at, Level: logger.LevelInfo, Message: "dial postgres://bob:pw2@db/y", Attrs: own,
		})
		if string(spliced) != string(whole) {
			t.Fatalf("spliced line:\n%s\nwhole-record line:\n%s", spliced, whole)
		}
		var out bytes.Buffer
		sink, err := logger.NewWriterSink(&out)
		if err != nil {
			t.Fatal(err)
		}
		lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: redacting(c.enc)})
		if err != nil {
			t.Fatal(err)
		}
		logger.Info(t.Context(), lg.With(bound...), "dial postgres://bob:pw2@db/y", own...)
		for _, leaked := range []string{"hunter2", "ann:pw", "bob:pw2", "n1", "h1", "t0k3n"} {
			if strings.Contains(out.String(), leaked) {
				t.Errorf("%s leaked: %s", leaked, out.String())
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// secretThread is a recursive type with a secret at every depth.
type secretThread struct {
	Token   string                  `json:"token" app:"secret"`
	Amount  float64                 `json:"amount"`
	Replies []secretThread          `json:"replies,omitempty"`
	ByName  map[string]secretThread `json:"byName,omitempty"`
}

// A secret member is left out of an export, in every element and down a
// recursive type as deep as the document goes; numbers are kept as written.
func TestAnExportLeavesSecretsOut(t *testing.T) {
	doc := `{"token":"a","amount":1.50,"replies":[{"token":"b","amount":2,"replies":[{"token":"c","amount":3}]}],"byName":{"x":{"token":"d","amount":4}}}`
	out, err := planOf(reflect.TypeFor[secretThread]()).withoutSecrets([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"amount":1.50,"byName":{"x":{"amount":4}},"replies":[{"amount":2,"replies":[{"amount":3}]}]}`; string(out) != want {
		t.Errorf("without secrets: %s, want %s", out, want)
	}
}

// The journal's chain verifies, and breaks where an entry changed, or at
// the number of an entry removed.
func TestTheJournalChains(t *testing.T) {
	var entries []journalRecord
	prev := ""
	for i := range 4 {
		e := journalRecord{Seq: int64(i + 1), At: time.Date(2026, 9, 27, 0, 0, i, 0, time.UTC), Op: model.JournalErase, Store: "s", By: "cli"}
		e.Hash = chainHash(prev, e)
		prev = e.Hash
		entries = append(entries, e)
	}
	if c := verifyChain(entries); c.BrokenAt != 0 || c.Entries != 4 {
		t.Fatalf("an intact chain: %+v", c)
	}
	changed := slices.Clone(entries)
	changed[2].By = "someone else"
	if c := verifyChain(changed); c.BrokenAt != 3 {
		t.Errorf("a changed entry: %+v", c)
	}
	removed := slices.Delete(slices.Clone(entries), 1, 2)
	if c := verifyChain(removed); c.BrokenAt != 2 {
		t.Errorf("a removed entry: %+v", c)
	}
}
