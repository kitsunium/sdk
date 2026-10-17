package kit_test

import (
	// The suite's products read YAML and TOML configuration files, as a
	// product does: by importing the packages that read them.
	_ "github.com/kitsunium/sdk/framework/kit/config/toml"
	_ "github.com/kitsunium/sdk/framework/kit/config/yaml"
	// The suite's servers serve HTTP, as a product that imports it does.
	_ "github.com/kitsunium/sdk/framework/kit/server"
	// The suite's stores and databases run on the store subsystem, as a
	// product that imports it does (ADR 0179 §1) — internal tests included:
	// one binary. subsystems_internal_test.go takes it out to see what a
	// program that does not import it is refused.
	_ "github.com/kitsunium/sdk/framework/kit/store"
	// The suite's topics, subscriptions, watches, queued commands and
	// outboxes on a database run on the queue subsystem, as a product that
	// imports it does (ADR 0179 step 4); subsystems_internal_test.go takes it
	// out too.
	_ "github.com/kitsunium/sdk/framework/kit/queue"
	// The suite's workflows run on the workflow subsystem, as a product that
	// imports it does (ADR 0179 step 5); subsystems_internal_test.go takes
	// it out too.
	_ "github.com/kitsunium/sdk/framework/kit/workflow"
	// The suite's mailers run on the mail subsystem, as a product that
	// imports it does (ADR 0179 step 6); subsystems_internal_test.go takes
	// it out too.
	_ "github.com/kitsunium/sdk/framework/kit/mail"
	// The suite's dev apps serve the Studio's API, as a product that imports
	// it does.
	_ "github.com/kitsunium/sdk/framework/kit/studio"
)
