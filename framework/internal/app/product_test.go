package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// An item's states, in the order an item may pass through them; the zero
// State is none, an item's before its creation.
const (
	noState State = iota
	Draft
	Live
	Sold
	Expired
	Retired
)

// The product under test: a shop whose items live in a workflow, and an
// audit service that records every event of the shop and tallies it.
// Declarations are package-level, exactly as a product writes them; the
// positions test reads this file to know where each one is.

var Shop = app.NewService("shop", "A shop, for the tests.")

var Items = Shop.Store("items", func(i Item) string { return i.ID })

var Events = Shop.Topic[Event]("events")

var Lifecycle = Shop.Workflow("lifecycle", Items, func(i *Item) *State { return &i.State }).
	Initial(Draft).
	On("publish", Draft, Live).
	On("sell", Live, Sold).
	After("expire", time.Hour, Live, Expired).
	At("lapse", Live, Expired, expiry).
	When("sold-out", Live, Sold, soldOut).
	On("retire", Draft, Retired).
	OnEnter(Sold, markSold).
	OnEnter(Retired, deleteSelf).
	OnTransition(announce)

var (
	_ = Shop.Endpoint("POST /items", CreateItem, app.RateLimit(1000, 1000))
	_ = Shop.Endpoint("GET /items/{id}", GetItem)
	_ = Shop.Endpoint("PUT /items/{id}", RenameItem)
	_ = Shop.Endpoint("POST /items/{id}/{event}", FireItem)
	_ = Shop.Endpoint("GET /whoami", WhoAmI)
	_ = Shop.Endpoint("GET /boom", Boom)
	_ = Shop.Endpoint("POST /limited", Limited, app.RateLimit(1, 2), app.MaxBody(64))
	_ = Shop.Endpoint("DELETE /items/{id}", DeleteItem)
	_ = Shop.Endpoint("POST /notes", AddNote)

	// CountAPI counts the items; the audit's job runs it in process too
	// (Endpoint.Call).
	CountAPI = Shop.Endpoint("GET /count", Count)
)

// The endpoints a test replaces (app.Replace) are named.
var (
	SearchAPI = Shop.Endpoint("GET /search", Search)
	SlowAPI   = Shop.Endpoint("GET /slow", Slow, app.Timeout(20*time.Millisecond))
)

// Quote prices an item: the shop needs a price and does not decide it. Its
// list price, unless the app binds the port or a service implements it.
var Quote = Shop.Port[QuoteInput, Quoted]("quote", app.Fallback(ListPriceAPI))

var (
	_ = Shop.Endpoint("GET /items/{id}/quote", QuoteItem)

	// ListPriceAPI is the quote's fallback, on a route of its own too.
	ListPriceAPI = Shop.Endpoint("POST /list-price", ListPrice)
)

var Audit = app.NewService("audit", "Records every event of the shop.")

var Log = Audit.Store("log", func(e Entry) string { return e.ID })

var _ = Audit.Subscribe("record", Events, Record)

// flaky makes Record fail once for the item named "flaky", to prove retries.
var flaky = struct {
	sync.Mutex
	failed map[string]bool
}{failed: map[string]bool{}}

var _ = Audit.Every("tally", time.Minute, Tally)

// tallies counts the runs of the tally job.
var tallies = struct {
	sync.Mutex
	counts []int
}{}

var Members = app.NewService("members", "Accounts, sessions and mail, for the tests of the framework.")

var Accounts = Members.Store("accounts", func(a Account) string { return a.ID },
	app.Unique("email", func(a Account) string { return a.Email }),
	app.Index("team", func(a Account) []string { return a.Teams }))

var Sessions = Members.Store("sessions", func(s Session) string { return s.ID },
	app.Unique("token", func(s Session) string { return s.Token }),
	app.Index("account", func(s Session) []string { return []string{s.Account} }))

var SessionAuth = Members.AuthHandler("session", Authenticate)

var (
	_ = Members.Endpoint("POST /login", Login)
	_ = Members.Endpoint("POST /logout", Logout, app.Auth())
	_ = Members.Endpoint("GET /greeting", Greet, app.AuthOptional())
	_ = Members.Endpoint("POST /invite", Invite, app.Auth())

	// WhoAPI answers an authenticated caller, over HTTP and in process:
	// EndpointService.Call carries the caller's user.
	WhoAPI = Members.Endpoint("GET /who", WhoIs, app.Auth())
)

// MeAPI is named: a test replaces it.
var MeAPI = Members.Endpoint("GET /me", Me, app.Auth())

// Mail is the members' outbound mail.
var Mail = Members.Mailer("mail", app.From("Members", "members@example.com"), app.MailAttempts(3))

// LinkKey signs the links the members hand out: kit makes it and rotates it.
var LinkKey = Members.Secret("link-key", app.Generated(32))

// Joined announces a new member.
var Joined = Members.Topic[Account]("joined")

// digests is what the tests make the digest loop do, and what it saw.
var digests = struct {
	sync.Mutex
	wakes  []app.WakeEvent
	due    time.Time     // what nextDigest answers; zero: no deadline
	fail   int           // how many runs fail
	panics int           // how many runs panic
	gate   chan struct{} // when set, a run waits for it
}{}

var Digest = Members.Loop("digest", RunDigest,
	app.WakeEvery(time.Hour), app.WakeAt(nextDigest), app.WakeOn(Joined))

var Janitor = Members.Go("janitor", Sweep)

// MemberQuoteAPI is the members' answer to the shop's quote.
var MemberQuoteAPI = Members.Implement(Quote, MemberPrice)

// State is where an item is in its life, which the workflow moves.
type State int

// Item is an article of the shop; its name, which others see, is moderated:
// the reviews module's watch hears it (watch_test.go).
type Item struct {
	ID      string     `json:"id"`
	Name    string     `json:"name" kit:"moderated"`
	Price   int32      `json:"price"`
	Stock   int        `json:"stock"`
	State   State      `json:"state"`
	SoldAt  *time.Time `json:"soldAt,omitempty"`
	Tags    []string   `json:"tags,omitempty"`
	Expires *time.Time `json:"expires,omitempty"`
}

type Event struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	To    State  `json:"to"`
}

type CreateInput struct {
	Name  string   `json:"name" validate:"required,maxlen=20"`
	Price int32    `json:"price" validate:"min=1"`
	Stock int      `json:"stock"`
	Tags  []string `json:"tags,omitempty"`
}

type ByID struct {
	ID string `path:"id"`
}

type RenameInput struct {
	ID   string `path:"id"`
	Name string `json:"name"`
}

type FireInput struct {
	ID    string `path:"id"`
	Event string `path:"event"`
}

type SearchInput struct {
	Q      string        `query:"q"`
	Limit  int8          `query:"limit"`
	Since  *time.Time    `query:"since"`
	Within time.Duration `query:"within"`
	Tags   []string      `query:"tag"`
	Exact  bool          `query:"exact"`
}

type WhoInput struct {
	Agent string `header:"X-Agent"`
}

type LimitedInput struct {
	Note string `json:"note"`
}

// Auth is embedded: its header-bound field must stay out of the body's reach.
type Auth struct {
	User string `header:"X-User"`
}

type NoteInput struct {
	Auth
	Text string `json:"text"`
}

type CountOutput struct {
	Items int `json:"items"`
}

// QuoteInput asks the price of one item.
type QuoteInput struct {
	ID string `json:"id" validate:"required"`
}

// Quoted is a price, and who set it.
type Quoted struct {
	Price int32  `json:"price"`
	By    string `json:"by"`
}

type Entry struct {
	ID    string `json:"id"`
	Event string `json:"event"`
}

// Account is a member. Email is unique; an account belongs to any number of
// teams.
type Account struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Teams []string `json:"teams,omitempty"`
}

// Session is a signed-in account.
type Session struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Account string `json:"account"`
}

// Credentials are what a request proves itself with: the session cookie, or
// a bearer token.
type Credentials struct {
	Cookie string `cookie:"sid"`
	Bearer string `header:"Authorization"`
}

// Who is what the auth handler says about the caller.
type Who struct {
	Name string `json:"name"`
}

type LoginInput struct {
	Email string `json:"email" validate:"required"`
}

// Profile is an account as the caller sees it, with where the request came
// from.
type Profile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IP    string `json:"ip,omitempty"`
	Agent string `json:"agent,omitempty"`
}

type GreetInput struct {
	Theme string `cookie:"theme"`
}

type Greeting struct {
	Text  string `json:"text"`
	Theme string `json:"theme,omitempty"`
}

type InviteInput struct {
	Email   string `json:"email"`
	Subject string `json:"subject"`
}

type Invited struct {
	Mail string `json:"mail"`
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func soldOut(i Item) bool { return i.Stock == 0 }

// expiry is when a live item lapses: its own expiry date, if it has one.
func expiry(i Item) (time.Time, bool) {
	if i.Expires == nil {
		return time.Time{}, false
	}
	return *i.Expires, true
}

func markSold(_ context.Context, i *Item) error {
	i.SoldAt = new(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return nil
}

// deleteSelf deletes the entity while its transition is running: the
// transition must fail rather than write it back.
func deleteSelf(ctx context.Context, i *Item) error { return Items.Delete(ctx, i.ID) }

func announce(ctx context.Context, c app.ChangeEvent[Item, State]) error {
	return Events.Publish(ctx, Event{ID: c.Key, Event: c.Event, To: c.To})
}

// CreateItem drafts an item.
func CreateItem(ctx context.Context, in CreateInput) (Item, error) {
	return Lifecycle.Start(ctx, Item{ID: app.NewID("item"), Name: in.Name, Price: in.Price, Stock: in.Stock, Tags: in.Tags})
}

// GetItem reads one item.
func GetItem(ctx context.Context, in ByID) (Item, error) { return Items.Get(ctx, in.ID) }

// RenameItem renames an item; the path decides which one.
func RenameItem(ctx context.Context, in RenameInput) (Item, error) {
	return Items.Update(ctx, in.ID, func(i *Item) error { i.Name = in.Name; return nil })
}

// FireItem fires a lifecycle event.
func FireItem(ctx context.Context, in FireInput) (Item, error) {
	return Lifecycle.Fire(ctx, in.ID, in.Event)
}

// Search echoes what it was asked.
func Search(_ context.Context, in SearchInput) (SearchInput, error) { return in, nil }

// WhoAmI echoes a header.
func WhoAmI(_ context.Context, in WhoInput) (WhoInput, error) { return in, nil }

// Boom panics with something that must never reach the caller.
func Boom(context.Context, app.EmptyValue) (app.EmptyValue, error) {
	panic("canary: do-not-leak-7f3a9c")
}

// Slow outlives its timeout.
func Slow(ctx context.Context, _ app.EmptyValue) (app.EmptyValue, error) {
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}
	return app.EmptyValue{}, ctx.Err()
}

// Limited is rate limited and takes a tiny body.
func Limited(_ context.Context, in LimitedInput) (LimitedInput, error) { return in, nil }

// AddNote echoes who wrote what.
func AddNote(_ context.Context, in NoteInput) (NoteInput, error) { return in, nil }

// DeleteItem deletes an item.
func DeleteItem(ctx context.Context, in ByID) (app.EmptyValue, error) {
	return app.EmptyValue{}, Items.Delete(ctx, in.ID)
}

// Count counts the items.
func Count(ctx context.Context, _ app.EmptyValue) (CountOutput, error) {
	n, err := Items.Count(ctx)
	return CountOutput{Items: n}, err
}

// QuoteItem asks the quote port the price of an item.
func QuoteItem(ctx context.Context, in ByID) (Quoted, error) {
	return Quote.Call(ctx, QuoteInput(in))
}

// ListPrice is an item's own price.
func ListPrice(ctx context.Context, in QuoteInput) (Quoted, error) {
	it, err := Items.Get(ctx, in.ID)
	if err != nil {
		return Quoted{}, err
	}
	return Quoted{Price: it.Price, By: "list"}, nil
}

// Record keeps each event once, whatever the number of deliveries.
func Record(ctx context.Context, e Event) error {
	it, err := Items.Get(ctx, e.ID)
	if err == nil && it.Name == "flaky" {
		flaky.Lock()
		first := !flaky.failed[e.ID+e.Event]
		flaky.failed[e.ID+e.Event] = true
		flaky.Unlock()
		if first {
			return errors.New("transient failure")
		}
	}
	return Log.Put(ctx, Entry{ID: e.ID + "." + e.Event, Event: e.Event})
}

// Tally asks the shop how many items it holds.
func Tally(ctx context.Context) error {
	out, err := CountAPI.Call(ctx, app.EmptyValue{})
	if err != nil {
		return err
	}
	tallies.Lock()
	tallies.counts = append(tallies.counts, out.Items)
	tallies.Unlock()
	return nil
}

// Authenticate resolves a session to its account, and slides the cookie.
func Authenticate(ctx context.Context, c Credentials) (app.UID, Who, error) {
	token, viaCookie := c.Cookie, true
	if bearer, ok := strings.CutPrefix(c.Bearer, "Bearer "); ok {
		token, viaCookie = bearer, false
	}
	if token == "" {
		return "", Who{}, nil
	}
	if token == "panic" {
		panic("canary: do-not-leak-auth-1b2c")
	}
	s, err := Sessions.Lookup(ctx, "token", token)
	if err != nil {
		app.ClearCookie(ctx, "sid")
		return "", Who{}, app.Unauthenticated("the session is not valid")
	}
	acc, err := Accounts.Get(ctx, s.Account)
	if err != nil {
		return "", Who{}, err
	}
	if viaCookie {
		app.SetCookie(ctx, &http.Cookie{Name: "sid", Value: token, MaxAge: 3600})
	}
	return app.UID(acc.ID), Who{Name: acc.Name}, nil
}

// Login opens a session for the account with that e-mail.
func Login(ctx context.Context, in LoginInput) (Profile, error) {
	acc, err := Accounts.Lookup(ctx, "email", in.Email)
	if err != nil {
		return Profile{}, app.Unauthenticated("no such account")
	}
	token := app.NewID("tok")
	if err := Sessions.Insert(ctx, Session{ID: app.NewID("sess"), Token: token, Account: acc.ID}); err != nil {
		return Profile{}, err
	}
	app.SetCookie(ctx, &http.Cookie{Name: "sid", Value: token, MaxAge: 3600})
	return Profile{ID: acc.ID, Name: acc.Name}, nil
}

// Me says who the caller is, and from where.
func Me(ctx context.Context, _ app.EmptyValue) (Profile, error) {
	uid, _ := app.UserID(ctx)
	who, _ := app.AuthData[Who](ctx)
	return Profile{ID: string(uid), Name: who.Name, IP: app.ClientIP(ctx), Agent: app.UserAgent(ctx)}, nil
}

// Logout ends the caller's sessions and forgets the cookie.
func Logout(ctx context.Context, _ app.EmptyValue) (app.EmptyValue, error) {
	uid, _ := app.UserID(ctx)
	sessions, err := Sessions.Find(ctx, "account", string(uid))
	if err != nil {
		return app.EmptyValue{}, err
	}
	for _, s := range sessions {
		if err := Sessions.Delete(ctx, s.ID); err != nil {
			return app.EmptyValue{}, err
		}
	}
	app.ClearCookie(ctx, "sid")
	return app.EmptyValue{}, nil
}

// Greet greets the caller, known or not.
func Greet(ctx context.Context, in GreetInput) (Greeting, error) {
	if who, ok := app.AuthData[Who](ctx); ok {
		return Greeting{Text: "hello, " + who.Name, Theme: in.Theme}, nil
	}
	return Greeting{Text: "hello, stranger", Theme: in.Theme}, nil
}

// Invite mails someone a link to join.
func Invite(ctx context.Context, in InviteInput) (Invited, error) {
	who, _ := app.AuthData[Who](ctx)
	id, err := Mail.Send(ctx, mail.Message{
		To:      []mail.Address{{Name: "Guest", Addr: in.Email}},
		Subject: in.Subject,
		Text:    who.Name + " invites you: https://members.example.com/join?token=tok_42",
		HTML:    "<p>" + who.Name + " invites you.</p>",
	})
	return Invited{Mail: id}, err
}

func resetDigests() {
	digests.Lock()
	defer digests.Unlock()
	digests.wakes, digests.due, digests.fail, digests.panics, digests.gate = nil, time.Time{}, 0, 0, nil
}

// RunDigest counts the members, and records why it ran.
func RunDigest(ctx context.Context, w app.WakeEvent) error {
	digests.Lock()
	digests.wakes = append(digests.wakes, w)
	fail, crash, gate := digests.fail > 0, digests.panics > 0, digests.gate
	if fail {
		digests.fail--
	}
	if crash {
		digests.panics--
	}
	digests.Unlock()
	if gate != nil {
		<-gate
	}
	if crash {
		panic("canary: do-not-leak-loop-9d8e")
	}
	if fail {
		return errors.New("the digest failed")
	}
	_, err := Accounts.Count(ctx)
	return err
}

// nextDigest is when the next digest is due, when one is.
func nextDigest(context.Context) (time.Time, bool) {
	digests.Lock()
	defer digests.Unlock()
	return digests.due, !digests.due.IsZero()
}

// Sweep looks for idle sessions every hour, until the app stops.
func Sweep(ctx context.Context) error {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if _, err := Sessions.Filter(ctx, func(Session) bool { return false }); err != nil {
				return err
			}
		}
	}
}

// MemberPrice is what a member pays: a tenth off the list price, said by
// the member it was quoted to.
func MemberPrice(ctx context.Context, in QuoteInput) (Quoted, error) {
	it, err := Items.Get(ctx, in.ID)
	if err != nil {
		return Quoted{}, err
	}
	uid, ok := app.UserID(ctx)
	if !ok {
		return Quoted{Price: it.Price, By: "guest"}, nil
	}
	return Quoted{Price: it.Price * 9 / 10, By: string(uid)}, nil
}

// WhoIs tells another service who it acts for.
func WhoIs(ctx context.Context, _ app.EmptyValue) (Profile, error) {
	uid, _ := app.UserID(ctx)
	who, _ := app.AuthData[Who](ctx)
	return Profile{ID: string(uid), Name: who.Name}, nil
}

// start runs the product for one test, in memory, on a free port, in dev.
// Mail is captured whatever the environment of the machine running the
// tests: a test never sends a real mail.
func start(t *testing.T, opts ...app.AppConfigurer) *app.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	a := app.NewApp("shop", Shop, Audit, Members).With(append([]app.AppConfigurer{
		app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a
}

// startManual runs the product on a manual clock.
func startManual(t *testing.T) (*app.App, *clock.ManualClock) {
	t.Helper()
	clk := clock.NewManualClock(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC))
	return start(t, app.Clock(clk)), clk
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	r.json(t, &e)
	return e.Error.Code
}

// none is the type of noBody.
type none struct{}

// noBody is the body of a request that sends none.
var noBody *none

// call sends one request to the app, route being "METHOD /path". body is
// JSON-encoded unless it is a string, sent as is, or noBody.
func call[B any](t *testing.T, a *app.App, route string, body B, headers ...string) response {
	method, path, _ := strings.Cut(route, " ")
	t.Helper()
	var rd io.Reader
	switch b := any(body).(type) {
	case *none:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, rawErr := json.Marshal(b)
		if rawErr != nil {
			t.Fatal(rawErr)
		}
		rd = bytes.NewReader(raw)
	}
	req, reqErr := http.NewRequestWithContext(t.Context(), method, a.URL()+path, rd)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, respErr := http.DefaultClient.Do(req)
	if respErr != nil {
		t.Fatal(respErr)
	}
	defer resp.Body.Close()
	raw, rawErr := io.ReadAll(resp.Body)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: raw}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pendinger is a clock that counts its armed timers.
type pendinger interface {
	Pending() int
}

// armed waits until n timers are armed on clk — every wait of the app, the
// loop the test drives included — so that the test moves the clock only
// once that loop sleeps. A timer counts from the instant it is armed: moved
// before, or between the loop's reading of the clock and its timer, the
// clock puts the timer past the instant it moved to, and the run never
// comes.
func armed(t *testing.T, clk pendinger, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d timers armed on the clock", n), func() bool { return clk.Pending() == n })
}

// create drafts an item through the API.
func create(t *testing.T, a *app.App, name string, stock int) Item {
	t.Helper()
	r := call(t, a, "POST /items", CreateInput{Name: name, Price: 10, Stock: stock})
	if r.status != http.StatusOK {
		t.Fatalf("create %q: %d %s", name, r.status, r.body)
	}
	var it Item
	r.json(t, &it)
	return it
}

// stateNames are the states' names, as the diagram and the history say them.
var stateNames = [...]string{noState: "", Draft: "draft", Live: "live", Sold: "sold", Expired: "expired", Retired: "retired"}

// String is the state's name.
func (s State) String() string { return stateNames[s] }
