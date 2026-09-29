// A node of the graph: one building block, its identity, where it was
// declared, and the details of its kind.

package core

// NodeEntity is one building block.
// Its ID is its identity; exactly one of the kind-specific fields matching
// Kind is set.
type NodeEntity struct {
	// ID is "<service>/<kind>/<name>", "<service>" for a service,
	// "binary:<name>" for a binary, "binary:<binary>/role/<name>" for a
	// role, "library:<name>" for a library, or [ExternalID]: the grammar of
	// [IDValue].
	ID string `json:"id"`
	// Kind says which building block this is.
	Kind NodeKind `json:"kind"`
	// Name is unique among the nodes of one kind in one service.
	Name string `json:"name"`
	// Service is the owning service ID. Empty for services and external.
	Service string `json:"service,omitempty"`
	// Module is the module the node belongs to — a module's service and
	// every node of it —; empty for the product's own.
	Module string `json:"module,omitempty"`
	// Doc is the Go doc comment of the declaration or of its handler, in
	// the product's default language.
	Doc string `json:"doc,omitempty"`
	// Docs are the same description in other languages, by tag ("fr"),
	// from the comment's tagged paragraphs: see [SplitDoc].
	Docs map[string]string `json:"docs,omitempty"`
	// Source is where the node is declared.
	Source *SourceMessage `json:"source,omitempty"`
	// Handler is the body of the function the node runs, when it has one.
	Handler *SourceMessage `json:"handler,omitempty"`

	Endpoint     *EndpointSpec     `json:"endpoint,omitempty"`
	Store        *StoreSpec        `json:"store,omitempty"`
	Topic        *TopicSpec        `json:"topic,omitempty"`
	Subscription *SubscriptionSpec `json:"subscription,omitempty"`
	Workflow     *WorkflowSpec     `json:"workflow,omitempty"`
	Job          *JobSpec          `json:"job,omitempty"`
	Frontend     *FrontendSpec     `json:"frontend,omitempty"`
	Auth         *AuthSpec         `json:"auth,omitempty"`
	Mailer       *MailerSpec       `json:"mailer,omitempty"`
	Loop         *LoopSpec         `json:"loop,omitempty"`
	Secret       *SecretSpec       `json:"secret,omitempty"`
	Port         *PortSpec         `json:"port,omitempty"`
	Command      *CommandSpec      `json:"command,omitempty"`
	Query        *QuerySpec        `json:"query,omitempty"`
	Binary       *BinarySpec       `json:"binary,omitempty"`
	Role         *RoleSpec         `json:"role,omitempty"`
	CLI          *CLISpec          `json:"cli,omitempty"`
	Listener     *ListenerSpec     `json:"listener,omitempty"`
	Library      *LibrarySpec      `json:"library,omitempty"`
	Presentation *PresentationSpec `json:"presentation,omitempty"`

	// Code is the code the node runs, as the static analysis read it: C4's
	// code level.
	Code *CodeResult `json:"code,omitempty"`

	// Stats are runtime counters, present on a runtime graph.
	Stats *StatsMessage `json:"stats,omitempty"`
}
