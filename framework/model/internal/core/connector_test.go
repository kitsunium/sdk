package core

import (
	"slices"
	"testing"
)

// A product's connectors come from what it declares: nothing web about a
// product without endpoints.
func TestConnectorsComeFromTheNodes(t *testing.T) {
	nodes := []NodeEntity{
		{ID: "tasks/endpoint/Create", Kind: KindEndpoint},
		{ID: "web/frontend/app", Kind: KindFrontend},
		{ID: "tasks/store/tasks", Kind: KindStore, Store: &StoreSpec{Backend: "file"}},
		{ID: "cache/store/hot", Kind: KindStore, Store: &StoreSpec{Backend: "memory"}},
		{ID: "notify/mailer/mail", Kind: KindMailer, Mailer: &MailerSpec{Transport: TransportCapture}},
		{ID: "tasks/topic/events", Kind: KindTopic, Topic: &TopicSpec{Broker: "file"}},
		{ID: "notify/subscription/mail", Kind: KindSubscription},
		{ID: "tasks/workflow/lifecycle", Kind: KindWorkflow},
	}
	got := ConnectorsOf(nodes)
	ids := make([]string, len(got))
	for i, c := range got {
		ids[i] = c.ID
	}
	if !slices.Equal(ids, []string{ConnectorHTTP, ConnectorMail, ConnectorQueue, ConnectorStore}) {
		t.Fatalf("connectors %v", ids)
	}
	byID := map[string]ConnectorMessage{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if c := byID[ConnectorStore]; c.Kind != ConnectorData || c.Detail != "file, memory" || !slices.Equal(c.Nodes, []string{"cache/store/hot", "tasks/store/tasks"}) {
		t.Errorf("store %+v", c)
	}
	if c := byID[ConnectorHTTP]; c.Kind != ConnectorTransport || len(c.Nodes) != 2 {
		t.Errorf("http %+v", c)
	}
	if c := byID[ConnectorMail]; c.Detail != TransportCapture {
		t.Errorf("mail %+v", c)
	}
	if c := byID[ConnectorQueue]; c.Detail != "file" || len(c.Nodes) != 2 {
		t.Errorf("queue %+v", c)
	}

	// Roles follow the process boundary: HTTP comes in, mail goes out, and
	// kit's own queues and stores stay inside — storage, never an entry point.
	roles := map[string][]string{}
	for _, c := range got {
		roles[c.ID] = c.Roles
	}
	if !slices.Equal(roles[ConnectorHTTP], []string{RoleInbound}) || !slices.Equal(roles[ConnectorMail], []string{RoleOutbound}) ||
		!slices.Equal(roles[ConnectorQueue], []string{RoleStorage}) || !slices.Equal(roles[ConnectorStore], []string{RoleStorage}) {
		t.Errorf("roles %v", roles)
	}
	if byID[ConnectorHTTP].Protocol != "http" || byID[ConnectorMail].Protocol != "" {
		t.Errorf("only the code fixes a protocol: %+v", got)
	}
	// A connector owns only its own settings: the data directory's and kit's
	// port's belong to them.
	if !slices.Equal(byID[ConnectorMail].Settings, []string{VarSMTPURL}) || len(byID[ConnectorStore].Settings) != 0 || len(byID[ConnectorHTTP].Settings) != 0 {
		t.Errorf("settings %+v", got)
	}

	// A command-line tool: a loop and a store, no transport at all.
	cli := ConnectorsOf([]NodeEntity{{ID: "lint/loop/run", Kind: KindLoop}, {ID: "lint/store/cache", Kind: KindStore, Store: &StoreSpec{Backend: "file"}}})
	if len(cli) != 1 || cli[0].ID != ConnectorStore {
		t.Errorf("a product without endpoints: %+v", cli)
	}

	// Every graph carries them once normalized, and they never move its revision.
	g := &GraphMessage{App: AppMessage{Name: "todo"}, Nodes: nodes}
	g.Normalize()
	if len(g.Connectors) != 4 {
		t.Errorf("normalized graph: %+v", g.Connectors)
	}
	if len(g.ConnectorCatalog) != len(builtin) {
		t.Errorf("the catalog of available connectors: %+v", g.ConnectorCatalog)
	}
	rev := g.Revision
	g.Connectors = nil
	g.Normalize()
	if g.Revision != rev {
		t.Errorf("revision moved: %s → %s", rev, g.Revision)
	}
}

// The catalog is every connector kit ships, and what brings each: a product
// that declares none still learns what it could plug into.
func TestTheConnectorCatalogSaysWhatBringsEach(t *testing.T) {
	cat := ConnectorCatalog()
	ids := make([]string, len(cat))
	for i, c := range cat {
		ids[i] = c.ID
		if len(c.Roles) == 0 || len(c.Declares) == 0 {
			t.Errorf("%s: roles %v, declares %v", c.ID, c.Roles, c.Declares)
		}
	}
	if !slices.Equal(ids, []string{ConnectorDatabase, ConnectorHTTP, ConnectorMail, ConnectorQueue, ConnectorStore}) {
		t.Fatalf("catalog %v", ids)
	}
	// A copy: the caller cannot change kit's table.
	cat[0].Roles[0] = "tampered"
	cat[0].Adapters[0].Import = "tampered"
	if again := ConnectorCatalog()[0]; again.Roles[0] == "tampered" || again.Adapters[0].Import == "tampered" {
		t.Error("the catalog shares kit's table")
	}
	// The database connector lists its engines and the import that adds each.
	var ids2 []string
	for _, e := range ConnectorCatalog()[0].Adapters {
		ids2 = append(ids2, e.ID)
		if e.Name == "" || e.Import != "github.com/kitsunium/sdk/framework/connectors/"+e.ID || e.Driver == "" {
			t.Errorf("engine %+v", e)
		}
	}
	if !slices.Equal(ids2, []string{EngineMySQL, EnginePostgres, EngineSQLite}) {
		t.Errorf("engines %v", ids2)
	}
}

// A store a database keeps brings two connectors: the store, and the
// database — whose detail lists the engines its stores run on, none while
// they stay in the data directory.
func TestAStoreADatabaseKeepsBringsTheDatabase(t *testing.T) {
	nodes := []NodeEntity{
		{ID: "desk/store/cases", Kind: KindStore, Store: &StoreSpec{Backend: "file", Database: "database"}},
		{ID: "audit/store/log", Kind: KindStore, Store: &StoreSpec{Backend: EnginePostgres, Database: "archive"}},
		{ID: "cache/store/hot", Kind: KindStore, Store: &StoreSpec{Backend: "memory"}},
	}
	byID := map[string]ConnectorMessage{}
	for _, c := range ConnectorsOf(nodes) {
		byID[c.ID] = c
	}
	db, ok := byID[ConnectorDatabase]
	if !ok || db.Kind != ConnectorData || !slices.Equal(db.Roles, []string{RoleOutbound, RoleStorage}) {
		t.Fatalf("database %+v", db)
	}
	if !slices.Equal(db.Nodes, []string{"audit/store/log", "desk/store/cases"}) || db.Detail != EnginePostgres {
		t.Errorf("database %+v", db)
	}
	if st := byID[ConnectorStore]; len(st.Nodes) != 3 || st.Detail != "file, memory, postgres" {
		t.Errorf("store %+v", st)
	}
	if len(ConnectorsOf(nodes[2:])) != 1 {
		t.Error("a store no database keeps brings the database connector")
	}
}

// A queued command waits in a queue of its own and brings the queue
// connector, its detail where the queue lives; a command handled on its
// caller's goroutine, like a query, brings none (ADR 0005).
func TestAQueuedCommandBringsTheQueueConnector(t *testing.T) {
	sync := NodeEntity{ID: "orders/command/place", Kind: KindCommand, Command: &CommandSpec{Mode: ModeSync}}
	ask := NodeEntity{ID: "orders/query/mine", Kind: KindQuery, Query: &QuerySpec{}}
	if got := ConnectorsOf([]NodeEntity{sync, ask}); len(got) != 0 {
		t.Fatalf("a synchronous command and a query bring %+v", got)
	}
	queued := NodeEntity{ID: "orders/command/reindex", Kind: KindCommand, Command: &CommandSpec{Mode: ModeQueued, Queue: "file"}}
	got := ConnectorsOf([]NodeEntity{sync, ask, queued})
	if len(got) != 1 || got[0].ID != ConnectorQueue || got[0].Detail != "file" || !slices.Equal(got[0].Nodes, []string{queued.ID}) {
		t.Fatalf("a queued command brings %+v", got)
	}
	var queue ConnectorSpec
	for _, c := range ConnectorCatalog() {
		if c.ID == ConnectorQueue {
			queue = c
		}
	}
	if !slices.Contains(queue.Declares, KindCommand) || queue.brings != nil {
		t.Errorf("the catalog says what brings a queue, and hides kit's predicate: %+v", queue)
	}
}
