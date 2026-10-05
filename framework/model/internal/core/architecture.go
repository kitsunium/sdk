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
