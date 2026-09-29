// The C4 view of a product: its people, systems, containers, ports and links.

package core

// Container kinds.
const (
	ContainerProcess = "process" // the Go binary: every service, one process
	ContainerSPA     = "spa"     // a frontend: served by the process, run by a browser
	ContainerVolume  = "volume"  // the data directory: store files, queue files
	ContainerMemory  = "memory"  // data kept in the process's memory only
	// ContainerDatabase is a database the app declares (kit.Database):
	// "container:database:<name>", drawn as a cylinder.
	ContainerDatabase = "database"
)

// Whose a port is.
const (
	// PortOwnerKit is kit's own listener: the health probes, the Studio in
	// dev, and the product's HTTP when it declares any.
	PortOwnerKit = "kit"
)

// What a port serves.
const (
	ServesHealth = "health" // the liveness and readiness probes
	ServesStudio = "studio" // the Studio and its API, in dev
	ServesHTTP   = "http"   // the product's own HTTP entry points
)

// ArchitectureMessage is the product at the two outer levels of the C4 model.
//
// Context (level 1) is the product as one box, the people who use it and the
// systems it depends on. Containers (level 2) is what the product is
// deployed as: the process, the frontend its browser runs, the volume its
// data lives on. Components (level 3) are the nodes, grouped by service; code
// (level 4) is each node's [CodeResult].
//
// kit derives all of it from the declarations — a frontend implies a person
// with a browser, a mailer an SMTP relay, a data directory a volume, a
// database its own container — so it is exactly as true as the rest of the
// graph.
type ArchitectureMessage struct {
	// People are the humans the product serves.
	People []PersonMessage `json:"people"`
	// Systems are the software systems outside the product it talks to, or
	// that talk to it.
	Systems []SystemMessage `json:"systems"`
	// Containers are the product's runnable and storage units.
	Containers []ContainerMessage `json:"containers"`
	// Links are the relationships between people, systems and containers.
	Links []LinkMessage `json:"links"`
}

// PersonMessage is a human user of the product.
// It is drawn at the edge of the C4 context view, outside every container.
type PersonMessage struct {
	// ID is unique in the architecture: "person:user".
	ID string `json:"id"`
	// Name is what the diagram prints: "User".
	Name string `json:"name"`
	// Doc says who they are and what they do with the product.
	Doc string `json:"doc,omitempty"`
}

// SystemMessage is a software system outside the product.
// Nodes are the product's nodes that talk to it; Technology says what it runs
// on.
type SystemMessage struct {
	// ID is unique in the architecture: "system:smtp".
	ID string `json:"id"`
	// Name is what the diagram prints: "SMTP relay".
	Name string `json:"name"`
	// Doc says what it is to the product.
	Doc string `json:"doc,omitempty"`
	// Technology names how it is reached: "SMTP · STARTTLS".
	Technology string `json:"technology,omitempty"`
	// Location says where, when it is known and not secret: "smtp.example.com:587".
	Location string `json:"location,omitempty"`
	// Nodes are the nodes of the product that talk to it.
	Nodes []string `json:"nodes,omitempty"`
}

// ContainerMessage is one runnable or storage unit of the product.
// Its ID starts with "container:"; Nodes are the nodes that live in it.
type ContainerMessage struct {
	// ID is unique in the architecture: "container:process".
	ID string `json:"id"`
	// Name is what the diagram prints.
	Name string `json:"name"`
	// Kind is one of the Container constants.
	Kind string `json:"kind"`
	// Technology names what it is built with: "Go 1.27 · kit · net/http".
	Technology string `json:"technology,omitempty"`
	// Doc says what it does.
	Doc string `json:"doc,omitempty"`
	// Location is where it runs or lives: the listen address, the data
	// directory. Present in dev only.
	Location string `json:"location,omitempty"`
	// Nodes are the nodes it hosts: services for the process, stores and
	// subscriptions for the volume, frontends for an SPA.
	Nodes []string `json:"nodes,omitempty"`
	// Ports are what the process listens on, and whose each port is.
	Ports []PortMessage `json:"ports,omitempty"`
	// Settings are the settings that configure it: KIT_DATA_DIR for the
	// data directory; a database's URL and tuning variables for a database.
	Settings []string `json:"settings,omitempty"`
	// Engine is a database's engine: "postgres", "mysql", "sqlite".
	Engine string `json:"engine,omitempty"`
	// Source is where the code declares the container, when it does: the
	// app for the process, a frontend for its SPA, kit.Database for a
	// database. [GraphMessage.Files] allows it.
	Source *SourceMessage `json:"source,omitempty"`
}

// PortMessage is an address a process listens on.
// Owner is the container that listens on it; Serves names what is reached
// through it.
type PortMessage struct {
	// Address is the listen address: "127.0.0.1:4000", ":4000". Present in
	// dev only, like every location.
	Address string `json:"address,omitempty"`
	// Owner is [PortOwnerKit], or the ID of the connector that opened it.
	Owner string `json:"owner"`
	// Serves says what it answers: the Serves constants.
	Serves []string `json:"serves"`
	// Settings are the settings that configure it.
	Settings []string `json:"settings,omitempty"`
}

// LinkMessage is a relationship between two elements of the architecture. The
// process's link to a database, "Reads and writes", carries the database
// connector, and the store connector once stores live on it.
type LinkMessage struct {
	// From is the ID of a person, system or container.
	From string `json:"from"`
	// To is the ID of a person, system or container.
	To string `json:"to"`
	// Label says what the relationship is: "Reads and writes".
	Label string `json:"label"`
	// Technology says how: "JSON over HTTP", "SMTP", "file I/O".
	Technology string `json:"technology,omitempty"`
	// Nodes are the nodes whose edges make the link true.
	Nodes []string `json:"nodes,omitempty"`
	// Connectors are the connectors the link is made of: one link from the
	// process to the data directory carries the stores, the queues and the
	// mail outbox.
	Connectors []string `json:"connectors,omitempty"`
}
