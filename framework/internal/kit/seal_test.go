package kit_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The sealing product (ADR 0006 §4): a clinic's patients, accounts and
// memos, sealed where they rest, its letters and notices, which wait in
// queues on disk, and its reminders, a queued command.

var Clinic = kit.NewService("clinic", "Patients, letters and reminders, for the sealing tests.")

// Patient is a person's file: every kind of member kit seals, and some it
// does not.
type Patient struct {
	ID       string            `json:"id"`
	Email    string            `json:"email" kit:"subject"`
	Name     string            `json:"name" kit:"personal"`
	Nick     string            `json:"nick,omitempty" kit:"personal,plain"`
	Blood    string            `json:"blood,omitempty" kit:"special"`
	Pregnant bool              `json:"pregnant" kit:"special"`
	Age      int               `json:"age" kit:"personal"`
	Born     *time.Time        `json:"born,omitempty" kit:"personal"`
	Token    string            `json:"token,omitempty" kit:"secret"`
	Home     Home              `json:"home" kit:"personal"`
	Visits   []Visit           `json:"visits,omitempty"`
	Tags     map[string]string `json:"tags,omitempty" kit:"personal"`
	Ward     string            `json:"ward" kit:"public"`
	Notes    string            `json:"notes,omitempty" kit:"personal,history=3"`
	ErasedAt *time.Time        `json:"erasedAt,omitempty" kit:"erased"`
}

// Home is where a patient lives.
type Home struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

// Visit is one of a patient's visits: its note is personal, its day is not.
type Visit struct {
	Day  string `json:"day"`
	Note string `json:"note" kit:"personal"`
}

func (p Patient) key() string { return p.ID }

var Patients = Clinic.Store("patients", Patient.key,
	kit.Unique("email", func(p Patient) string { return p.Email }),
	kit.Index("ward", func(p Patient) []string { return []string{p.Ward} }),
	kit.Purpose("Care for the patients"))

// ClinicAccount is keyed by its subject: its key reads a member kit seals,
// which stays in clear.
type ClinicAccount struct {
	ID       string `json:"id" kit:"subject"`
	Password string `json:"password" kit:"secret"`
	Bio      string `json:"bio" kit:"personal"`
}

var ClinicAccounts = Clinic.Store("accounts", func(a ClinicAccount) string { return a.ID }, kit.DeleteOnErasure())

// Memo is about nobody: sealed under a data key of its own.
type Memo struct {
	ID   string `json:"id"`
	Body string `json:"body" kit:"personal"`
}

var Memos = Clinic.Store("memos", func(m Memo) string { return m.ID })

// Letter is a message about a person: sealed under their key.
type Letter struct {
	To   string `json:"to" kit:"subject"`
	Body string `json:"body" kit:"personal"`
	Kind string `json:"kind"`
}

var Letters = Clinic.Topic[Letter]("letters")

// Notice is a message about nobody: sealed under data-key itself.
type Notice struct {
	Body string `json:"body" kit:"personal"`
}

var Notices = Clinic.Topic[Notice]("notices")

// Reminder is queued: sealed under the key of who dispatched it.
type Reminder struct {
	Text string `json:"text" kit:"personal"`
	Day  string `json:"day"`
}

// Forgetting names a person's identities.
type Forgetting struct {
	IDs []string `json:"ids" kit:"personal"`
}

// ClinicForget erases a person, as kit.Erase does.
var ClinicForget = Clinic.Command("forget", func(ctx context.Context, in Forgetting) (kit.Erasure, error) {
	return kit.Erase(ctx, "asked by the person", in.IDs...)
})

var (
	_ = Clinic.Subscribe("post", Letters, postLetter)
	// The shredder refuses every letter: each is dead-lettered at once,
	// and stays on disk, as a dead letter does.
	_      = Clinic.Subscribe("shredder", Letters, func(context.Context, Letter) error { return errors.New("refused") }, kit.MaxDeliveries(1))
	_      = Clinic.Subscribe("board", Notices, pinNotice)
	Remind = Clinic.Command("remind", remind, kit.Queued())
)

// received is what the clinic's consumers were handed, in order.
var received struct {
	sync.Mutex
	letters   []Letter
	notices   []Notice
	reminders []string
}

func postLetter(_ context.Context, l Letter) error {
	received.Lock()
	defer received.Unlock()
	received.letters = append(received.letters, l)
	return nil
}

func pinNotice(_ context.Context, n Notice) error {
	received.Lock()
	defer received.Unlock()
	received.notices = append(received.notices, n)
	return nil
}

func remind(ctx context.Context, r Reminder) (kit.EmptyValue, error) {
	uid, _ := kit.UserID(ctx)
	received.Lock()
	defer received.Unlock()
	received.reminders = append(received.reminders, string(uid)+": "+r.Text)
	return kit.EmptyValue{}, nil
}

// pinDataKeyWithoutFileStore pins data-key where the SDK keeps no protected
// file store — Windows, Plan 9 —: the secrets live in memory there, and kit
// refuses a store sealed on disk whose data-key would be made again at the
// next start.
func pinDataKeyWithoutFileStore(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	}
}

// startClinic runs the clinic on the data in dir, in dev.
func startClinic(t *testing.T, dir string, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	received.Lock()
	received.letters, received.notices, received.reminders = nil, nil, nil
	received.Unlock()
	app := kit.NewApp("clinic", Clinic).With(append([]kit.AppConfigurer{
		kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { stopClinic(t, app) })
	return app
}

// stopClinic stops the clinic; a clinic already stopped stays so.
func stopClinic(t *testing.T, app *kit.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil && !strings.Contains(err.Error(), "not running") {
		t.Errorf("stop: %v", err)
	}
}

// ann is a patient with every kind of member.
func ann() Patient {
	return Patient{
		ID: "p1", Email: "ann@clinic.test", Name: "Annabel Quist", Nick: "annie-q", Blood: "rhesus-negative",
		Pregnant: true, Age: 41, Born: new(time.Date(1986, 3, 4, 0, 0, 0, 0, time.UTC)), Token: "token-of-ann", Home: Home{Street: "12 Vesper Lane", City: "Lowmoor"},
		Visits: []Visit{{Day: "monday", Note: "complains of migraines"}}, Tags: map[string]string{"allergy": "penicillin"},
		Ward: "ward-east", Notes: "first notes",
	}
}

// secrets are the words no file of the data directory may hold: every
// sealed value the clinic writes.
var clinicSecrets = []string{
	"ann@clinic.test", "Annabel", "rhesus", "token-of-ann", "Vesper", "Lowmoor", "migraines",
	"penicillin", "first notes", "second notes", "hunter-two", "writes poems", "memo words", "letter words",
	"notice words", "reminder words",
}

// clinicFiles lists the files of the data directory — the secrets' store
// aside — that hold one of words.
func clinicFiles(t *testing.T, dir string, words ...string) []string {
	t.Helper()
	root, err := os.OpenRoot(dir)
	must(t, err)
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close %s: %v", dir, err)
		}
	}()
	return filesHolding(t, root.FS(), words...)
}

// Everything the clinic keeps on disk is sealed — its stores, a record's
// former values, the messages its queues keep, a dead letter — and reads
// back as it was written, after a restart too: by key, by index, by list,
// through a subscription and a queued command. A plain member, a public
// one and the key of a store keyed by its subject stay in clear.
func TestNothingPersonalRestsInClear(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := startClinic(t, dir)
	ctx := t.Context()
	must(t, Patients.Insert(ctx, ann()))
	_, err := Patients.Update(ctx, "p1", func(p *Patient) error { p.Notes = "second notes"; return nil })
	must(t, err)
	must(t, ClinicAccounts.Insert(ctx, ClinicAccount{ID: "acct-7", Password: "hunter-two", Bio: "writes poems"}))
	must(t, Memos.Insert(ctx, Memo{ID: "m1", Body: "memo words"}))
	must(t, Letters.Publish(ctx, Letter{To: "ann@clinic.test", Body: "letter words", Kind: "recall"}))
	must(t, Notices.Publish(ctx, Notice{Body: "notice words"}))
	_, err = Remind.Dispatch(kit.WithUser(ctx, "u-ann", kit.EmptyValue{}), Reminder{Text: "reminder words", Day: "friday"})
	must(t, err)
	eventually(t, "the deliveries", func() bool {
		received.Lock()
		defer received.Unlock()
		return len(received.letters) == 1 && len(received.notices) == 1 && len(received.reminders) == 1
	})
	checkDelivered(t)
	eventually(t, "the shredder's dead letter", func() bool {
		n := app.Graph().Node("clinic/subscription/shredder")
		return n != nil && n.Subscription.DeadLetters != nil && *n.Subscription.DeadLetters == 1
	})
	checkReadsBack(t)
	stopClinic(t, app)
	if held := clinicFiles(t, dir, clinicSecrets...); len(held) > 0 {
		t.Errorf("files hold sealed values in clear: %v", held)
	}
	// A queued reminder, handled, leaves its queue: the dead letter is what
	// stays.
	for _, word := range []string{"annie-q", "ward-east", "acct-7", "recall"} {
		if len(clinicFiles(t, dir, word)) == 0 {
			t.Errorf("%q, which kit does not seal, is nowhere in clear", word)
		}
	}
	must(t, app.Start(ctx))
	checkReadsBack(t)
}

// checkDelivered checks the consumers were handed the messages opened.
func checkDelivered(t *testing.T) {
	t.Helper()
	received.Lock()
	defer received.Unlock()
	if l := received.letters[0]; l != (Letter{To: "ann@clinic.test", Body: "letter words", Kind: "recall"}) {
		t.Errorf("the letter delivered: %+v", l)
	}
	if n := received.notices[0]; n.Body != "notice words" {
		t.Errorf("the notice delivered: %+v", n)
	}
	if r := received.reminders[0]; r != "u-ann: reminder words" {
		t.Errorf("the reminder handled: %q", r)
	}
}

// checkReadsBack reads what the clinic wrote, by every way a store reads.
func checkReadsBack(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	want := ann()
	want.Notes = "second notes"
	got, err := Patients.Get(ctx, "p1")
	must(t, err)
	if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
		t.Errorf("a patient reads back as %+v, want %+v", got, want)
	}
	checkIndexesRead(t, want)
	if former, err := Patients.Former(ctx, "p1", "/notes"); err != nil || len(former) != 1 || string(former[0].Value) != `"first notes"` {
		t.Errorf("a former value: %+v, %v", former, err)
	}
	if a, err := ClinicAccounts.Get(ctx, "acct-7"); err != nil || a.Password != "hunter-two" || a.Bio != "writes poems" {
		t.Errorf("an account keyed by its subject: %+v, %v", a, err)
	}
	if m, err := Memos.Get(ctx, "m1"); err != nil || m.Body != "memo words" {
		t.Errorf("a memo about nobody: %+v, %v", m, err)
	}
}

// checkIndexesRead reads the patients by their indexes and as a list.
func checkIndexesRead(t *testing.T, want Patient) {
	t.Helper()
	ctx := t.Context()
	if byMail, err := Patients.Lookup(ctx, "email", "ann@clinic.test"); err != nil || byMail.Name != want.Name {
		t.Errorf("the unique index on a sealed member: %+v, %v", byMail, err)
	}
	if inWard, err := Patients.Find(ctx, "ward", "ward-east"); err != nil || len(inWard) != 1 || inWard[0].Home != want.Home {
		t.Errorf("an index: %+v, %v", inWard, err)
	}
	if all, err := Patients.List(ctx); err != nil || len(all) != 1 || all[0].Tags["allergy"] != "penicillin" {
		t.Errorf("a list: %+v, %v", all, err)
	}
}

// A person's erasure reaches every copy of what their key sealed: a copy
// of the files taken before it opens nothing of them, their former values
// included, while their held record — moved under a key of its own when it
// was held — still opens where it lives now, and another person's records
// are untouched.
func TestAnErasureReachesEveryCopy(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := startClinic(t, dir)
	ctx := t.Context()
	must(t, Patients.Insert(ctx, ann()))
	_, err := Patients.Update(ctx, "p1", func(p *Patient) error { p.Notes = "second notes"; return nil })
	must(t, err)
	held := ann()
	held.ID, held.Email, held.Name = "p2", "ann+case@clinic.test", "Annabel Held"
	must(t, Patients.Insert(ctx, held))
	_, err = Patients.Update(ctx, "p2", func(p *Patient) error { p.Notes = "held notes"; return nil })
	must(t, err)
	must(t, Patients.Insert(ctx, Patient{ID: "p3", Email: "bob@clinic.test", Name: "Bob Keeps", Ward: "ward-west"}))
	backup := copyDir(t, filepath.Join(dir, "clinic"))
	must(t, Patients.Hold(ctx, "p2", "a court case"))

	done, err := ClinicForget.Dispatch(ctx, Forgetting{IDs: []string{"ann@clinic.test", "ann+case@clinic.test"}})
	must(t, err)
	if len(done.Stores) != 1 || !slices.Equal(done.Stores[0].Erased, []string{"p1"}) || !slices.Equal(done.Stores[0].Held, []string{"p2"}) {
		t.Fatalf("the erasure: %+v", done)
	}
	if p, err := Patients.Get(ctx, "p2"); err != nil || p.Name != "Annabel Held" || p.Home.City != "Lowmoor" {
		t.Errorf("the held record, after its person's erasure: %+v, %v", p, err)
	}
	if former, err := Patients.Former(ctx, "p2", "/notes"); err != nil || len(former) != 1 || string(former[0].Value) != `"first notes"` {
		t.Errorf("the held record's former values, after its person's erasure: %+v, %v", former, err)
	}
	checkShredJournaled(t, app)
	stopClinic(t, app)

	// The clinic's files as they were before the erasure, its keys as they
	// are now: a backup restored.
	must(t, os.RemoveAll(filepath.Join(dir, "clinic")))
	must(t, os.CopyFS(filepath.Join(dir, "clinic"), os.DirFS(backup)))
	must(t, app.Start(ctx))
	for _, key := range []string{"p1", "p2"} {
		checkErasedCopy(t, key)
	}
	if p, err := Patients.Get(ctx, "p3"); err != nil || p.Name != "Bob Keeps" {
		t.Errorf("bob's record, beside ann's erasure: %+v, %v", p, err)
	}
}

// checkErasedCopy checks ann's record under key, read from a copy of the
// files taken before her erasure: nothing sealed opens, its former values
// included, and what kit does not seal is there.
func checkErasedCopy(t *testing.T, key string) {
	t.Helper()
	ctx := t.Context()
	p, err := Patients.Get(ctx, key)
	if err != nil || p.Name != "" || p.Email != "" || p.Home != (Home{}) || p.Pregnant || len(p.Tags) > 0 || p.Visits[0].Note != "" {
		t.Errorf("ann's %s, from a copy taken before her erasure: %+v, %v", key, p, err)
	}
	if p.Ward != "ward-east" || p.Visits[0].Day != "monday" {
		t.Errorf("ann's %s lost what kit does not seal: %+v", key, p)
	}
	if former, err := Patients.Former(ctx, key, "/notes"); err != nil || len(former) != 0 {
		t.Errorf("ann's former values of %s, from a copy taken before her erasure: %+v, %v", key, former, err)
	}
}

// checkShredJournaled checks the journal says ann's keys were destroyed,
// by reference only.
func checkShredJournaled(t *testing.T, app *kit.App) {
	t.Helper()
	shreds := 0
	for _, e := range privacyOf(t, app).Journal {
		if e.Op == model.JournalShred {
			shreds++
			if e.Subject == "" || strings.Contains(e.Subject, "@") {
				t.Errorf("a shred entry: %+v", e)
			}
		}
	}
	if shreds != 2 {
		t.Errorf("%d shred entries, want one per identity", shreds)
	}
}

// copyDir copies dir to a new directory, and returns it.
func copyDir(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "copy")
	must(t, os.CopyFS(out, os.DirFS(dir)))
	return out
}

// The Studio never opens a sealed member: the data browser and a record's
// former values show it sealed; the graph, the logs and the Studio's
// answers hold no sealed value.
func TestTheStudioShowsSealedMembersSealed(t *testing.T) {
	needsFileStore(t)
	logs := &lockedBuffer{}
	app := startClinic(t, t.TempDir(), kit.Logs(logs))
	ctx := t.Context()
	must(t, Patients.Insert(ctx, ann()))
	_, err := Patients.Update(ctx, "p1", func(p *Patient) error { p.Notes = "second notes"; return nil })
	must(t, err)
	items := call(t, app, "GET /_kit/api/items?store=clinic/store/patients", noBody)
	var rows []map[string]any
	items.json(t, &rows)
	if len(rows) != 1 || rows[0]["name"] != model.SealedPlaceholder || rows[0]["home"] != model.SealedPlaceholder ||
		rows[0]["ward"] != "ward-east" || rows[0]["nick"] != "[redacted]" {
		t.Errorf("the data browser: %s", items.body)
	}
	former := call(t, app, "GET /_kit/api/former?store=clinic/store/patients&key=p1", noBody)
	if !strings.Contains(string(former.body), model.SealedPlaceholder) {
		t.Errorf("a former value sealed at rest: %s", former.body)
	}
	for what, raw := range map[string][]byte{"items": items.body, "former": former.body, "graph": mustJSON(t, app.Graph()), "logs": []byte(logs.String())} {
		if leaked := mentions(string(raw), clinicSecrets...); len(leaked) > 0 {
			t.Errorf("the %s show %v", what, leaked)
		}
	}
}

// The model says what kit seals: a store on disk's fields and its register
// line; a store in memory seals nothing.
func TestTheModelSaysWhatIsSealed(t *testing.T) {
	needsFileStore(t)
	for _, onDisk := range []bool{true, false} {
		opts := []kit.AppConfigurer{kit.InMemory()}
		if onDisk {
			opts = []kit.AppConfigurer{kit.DataDir(t.TempDir())}
		}
		app := startClinic(t, t.TempDir(), opts...)
		fields := map[string]bool{}
		for _, f := range app.Graph().Node("clinic/store/patients").Store.Entity.Fields {
			fields[f.Name] = f.Sealed
		}
		want := map[string]bool{
			"id": false, "email": onDisk, "name": onDisk, "nick": false, "blood": onDisk, "pregnant": onDisk, "age": onDisk,
			"born": onDisk, "token": onDisk, "home": onDisk, "visits": false, "tags": onDisk, "ward": false, "notes": onDisk, "erasedAt": false,
		}
		for name, sealed := range want {
			if fields[name] != sealed {
				t.Errorf("on disk %t: %s sealed %t, want %t", onDisk, name, fields[name], sealed)
			}
		}
		measure := model.MeasureNotSealed
		if onDisk {
			measure = model.MeasureSealed
		}
		for _, line := range privacyOf(t, app).Register.Stores {
			if line.Store == "clinic/store/patients" && !slices.Contains(line.Security, measure) {
				t.Errorf("on disk %t: the register says %v", onDisk, line.Security)
			}
		}
		stopClinic(t, app)
	}
}

// data-key rotates on its schedule, and each rotation re-wraps every data
// key under the new version — no box is touched: data-key keeps its three
// versions after four rotations, which only a re-wrap lets it prune, and
// every record still opens.
func TestDataKeyRotationRewrapsTheKeys(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("data-key rotates where the secrets are kept on disk: the SDK's file store needs file modes that are access lists")
	}
	clk := clock.NewManualClock(epoch)
	keep := kit.NewService("keep", "A store sealed on disk, for data-key's rotation.")
	holdings := keep.Store("holdings", func(h Holding) string { return h.ID })
	app := kit.NewApp("keep", keep).With(kit.DataDir(t.TempDir()), kit.Clock(clk), kit.Listen("127.0.0.1:0"),
		kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
	must(t, app.Start(t.Context()))
	t.Cleanup(func() { stopClinic(t, app) })
	ctx := t.Context()
	for _, h := range []Holding{{ID: "h1", Owner: "ann", Note: "ann's holding"}, {ID: "h2", Note: "nobody's holding"}} {
		must(t, holdings.Insert(ctx, h))
	}
	dataKey := func() *model.SecretInfo { return app.Graph().Node("kit.privacy/secret/data-key").Secret }
	for n := 1; n <= 4; n++ {
		armed(t, clk, 1)
		clk.Advance(30*24*time.Hour + time.Second)
		eventually(t, "a rotation", func() bool { return dataKey().Rotations == n })
		for _, key := range []string{"h1", "h2"} {
			if h, err := holdings.Get(ctx, key); err != nil || !strings.HasSuffix(h.Note, "holding") {
				t.Fatalf("after rotation %d, %s: %+v, %v", n, key, h, err)
			}
		}
	}
	if info := dataKey(); info.Version != 5 || info.Versions != 3 {
		t.Errorf("data-key after four rotations: version %d, %d kept, want 5 and 3", info.Version, info.Versions)
	}
}

// Holding is a record data-key's rotation keeps readable.
type Holding struct {
	ID    string `json:"id"`
	Owner string `json:"owner,omitempty" kit:"subject"`
	Note  string `json:"note" kit:"personal"`
}
