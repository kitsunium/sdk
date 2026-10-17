package kit

// The registration packages of the subsystems ADR 0179 takes out of kit's
// core, which the facade imports itself until the migration's last step
// (ADR 0179 Q8): a product that imports framework/kit has them all, and none
// changes before kit gen writes the imports it needs.
import (
	_ "github.com/kitsunium/sdk/framework/kit/mail"
	_ "github.com/kitsunium/sdk/framework/kit/queue"
	_ "github.com/kitsunium/sdk/framework/kit/store"
	_ "github.com/kitsunium/sdk/framework/kit/workflow"
)
