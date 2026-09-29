// The C4 view of a product: its people, systems, containers, ports and links.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// Container kinds.
const (
	ContainerProcess string = core.ContainerProcess // the Go binary: every service, one process
	ContainerSPA     string = core.ContainerSPA     // a frontend: served by the process, run by a browser
	ContainerVolume  string = core.ContainerVolume  // the data directory: store files, queue files
	ContainerMemory  string = core.ContainerMemory  // data kept in the process's memory only
	// ContainerDatabase is a database the app declares (kit.Database):
	// "container:database:<name>", drawn as a cylinder.
	ContainerDatabase string = core.ContainerDatabase
)

const (
	// PortOwnerKit is kit's own listener: the health probes, the Studio in
	// dev, and the product's HTTP when it declares any.
	PortOwnerKit string = core.PortOwnerKit
)

// What a port serves.
const (
	ServesHealth string = core.ServesHealth // the liveness and readiness probes
	ServesStudio string = core.ServesStudio // the Studio and its API, in dev
	ServesHTTP   string = core.ServesHTTP   // the product's own HTTP entry points
)

type (
	// Architecture is the product at the two outer levels of the C4 model.
	//
	// Context (level 1) is the product as one box, the people who use it and the
	// systems it depends on. Containers (level 2) is what the product is
	// deployed as: the process, the frontend its browser runs, the volume its
	// data lives on. Components (level 3) are the nodes, grouped by service; code
	// (level 4) is each node's [CodeInfo].
	//
	// kit derives all of it from the declarations — a frontend implies a person
	// with a browser, a mailer an SMTP relay, a data directory a volume, a
	// database its own container — so it is exactly as true as the rest of the
	// graph.
	Architecture = core.ArchitectureMessage
)

type (
	// Person is a human user of the product.
	// It is drawn at the edge of the C4 context view, outside every container.
	Person = core.PersonMessage
)

type (
	// System is a software system outside the product.
	// Nodes are the product's nodes that talk to it; Technology says what it runs
	// on.
	System = core.SystemMessage
)

type (
	// Container is one runnable or storage unit of the product.
	// Its ID starts with "container:"; Nodes are the nodes that live in it.
	Container = core.ContainerMessage
)

type (
	// Port is an address a process listens on.
	// Owner is the container that listens on it; Serves names what is reached
	// through it.
	Port = core.PortMessage
)

type (
	// Link is a relationship between two elements of the architecture. The
	// process's link to a database, "Reads and writes", carries the database
	// connector, and the store connector once stores live on it.
	Link = core.LinkMessage
)
