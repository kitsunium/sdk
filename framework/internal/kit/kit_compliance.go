package kit

import (
	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/statemachine"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// : Asserts at compile time every role a type is handed through an interface.
var (
	_ authenticator = (*AuthenticatorHandler[struct{}, struct{}])(nil)
	_ typeSource    = (*AuthenticatorHandler[struct{}, struct{}])(nil)

	_ exposed    = (*Command[struct{}, struct{}])(nil)
	_ starter    = (*Command[struct{}, struct{}])(nil)
	_ typeSource = (*Command[struct{}, struct{}])(nil)

	_ mounter    = (*EndpointService[struct{}, struct{}])(nil)
	_ typeSource = (*EndpointService[struct{}, struct{}])(nil)

	_ exposed    = (*Query[struct{}, struct{}])(nil)
	_ typeSource = (*Query[struct{}, struct{}])(nil)

	_ porter = (*PortService[struct{}, struct{}])(nil)

	_ Operation[struct{}, struct{}] = (*Command[struct{}, struct{}])(nil)
	_ Operation[struct{}, struct{}] = (*EndpointService[struct{}, struct{}])(nil)
	_ Operation[struct{}, struct{}] = (*PortService[struct{}, struct{}])(nil)
	_ Operation[struct{}, struct{}] = (*Query[struct{}, struct{}])(nil)

	_ plug.StoreEngine = (*docEngine)(nil)
	_ plug.StoreEngine = (*sqlEngine)(nil)
	_ plug.StoreEngine = (*sealedEngine)(nil)
	_ plug.StoreEngine = (*historied)(nil)

	_ statemachine.Store[struct{}] = storePort[struct{}]{}
	_ statemachine.Journal[string] = (*workflowJournal[struct{}, string])(nil)

	_ CommandConfigurer  = callOption(nil)
	_ EndpointConfigurer = callOption(nil)
	_ ExposeConfigurer   = callOption(nil)
	_ QueryConfigurer    = callOption(nil)

	_ ExposeConfigurer = endpointOption(nil)

	_ sealingSource = (*Command[struct{}, struct{}])(nil)

	_ CommandConfigurer      = deliveryOption(nil)
	_ SubscriptionConfigurer = deliveryOption(nil)

	_ AppConfigurer   = memoryOption{}
	_ StoreConfigurer = memoryOption{}

	_ AppConfigurer   = &bindOption{}
	_ MountConfigurer = &bindOption{}

	_ DatabaseConfigurer = migrations(nil)
	_ ModuleConfigurer   = migrations(nil)

	_ AppConfigurer = (*Module)(nil)
	_ Keeper        = (*Module)(nil)

	_ Keeper           = (*Service)(nil)
	_ ModuleConfigurer = (*Service)(nil)

	_ queueOwner = (*Command[struct{}, struct{}])(nil)
	_ queueOwner = (*SubscriptionWorker[struct{}])(nil)
	_ queueOwner = (*Watch)(nil)

	_ starter = (*Loop)(nil)
	_ starter = (*Routine)(nil)
	_ starter = (*Secret)(nil)
	_ starter = (*Watch)(nil)
	_ starter = (*SubscriptionWorker[struct{}])(nil)

	_ mailbox       = (*Mailer)(nil)
	_ starter       = (*Mailer)(nil)
	_ queueOwner    = (*Mailer)(nil)
	_ sealingSource = (*Mailer)(nil)

	_ queue.Waker             = (*sealedOutbox)(nil)
	_ queue.LeaseExtender     = (*sealedOutbox)(nil)
	_ queue.DeadLetterReader  = (*sealedOutbox)(nil)
	_ queue.Rejecter          = (*sealedOutbox)(nil)
	_ queue.DeadLetterManager = (*sealedOutbox)(nil)
	_ queue.MessageReader     = (*sealedOutbox)(nil)
	_ queue.MessageWriter     = (*sealedOutbox)(nil)
	_ queue.DeadLetterWriter  = (*sealedOutbox)(nil)
	_ queueReader             = (*sealedOutbox)(nil)
	_ queueWriter             = (*sealedOutbox)(nil)

	_ documenter = (*historied)(nil)
	_ documenter = (*sealedEngine)(nil)
	_ documenter = (*docEngine)(nil)
	_ documenter = (*sqlEngine)(nil)
	_ copyDocs   = (*docEngine)(nil)
	_ copySink   = (*sqlEngine)(nil)
	_ copyDocs   = (*jsonDocs)(nil)

	_ copyPartser   = (*StoreService[struct{}])(nil)
	_ journalParter = (*WorkflowService[struct{}, string])(nil)

	_ settingDecl = (*SettingService[string])(nil)

	_ mounter = (*Frontend)(nil)

	_ Keeper          = (*StoreService[struct{}])(nil)
	_ formerSource    = (*StoreService[struct{}])(nil)
	_ itemsSource     = (*StoreService[struct{}])(nil)
	_ marking         = (*StoreService[struct{}])(nil)
	_ privacyStore    = (*StoreService[struct{}])(nil)
	_ retentionRunner = (*StoreService[struct{}])(nil)
	_ trashRunner     = (*StoreService[struct{}])(nil)
	_ starter         = (*StoreService[struct{}])(nil)
	_ typeSource      = (*StoreService[struct{}])(nil)
	_ feeder          = (*StoreService[struct{}])(nil)
	_ sealingSource   = (*StoreService[struct{}])(nil)
	_ revisionsSource = (*StoreService[struct{}])(nil)
	_ trashSource     = (*StoreService[struct{}])(nil)
	_ sealAller       = (*StoreService[struct{}])(nil)
	_ tabled          = (*StoreService[struct{}])(nil)

	_ plug.Folder = (*docEngine)(nil)
	_ plug.Folder = (*historied)(nil)
	_ plug.Folder = (*sealedEngine)(nil)

	_ entriesSource = (*docEngine)(nil)
	_ entriesSource = (*sqlEngine)(nil)

	_ plug.Keyser = (*docEngine)(nil)
	_ plug.Keyser = (*sqlEngine)(nil)
	_ plug.Keyser = (*sealedEngine)(nil)
	_ plug.Keyser = (*historied)(nil)

	_ databaseSource = (*historied)(nil)
	_ databaseSource = (*sealedEngine)(nil)
	_ databaseSource = (*sqlEngine)(nil)

	_ plug.VersionKeeper = (*docEngine)(nil)
	_ plug.VersionKeeper = (*historied)(nil)
	_ plug.VersionKeeper = (*sealedEngine)(nil)
	_ plug.VersionKeeper = (*sqlEngine)(nil)

	// The engines that keep a trash, and those that hand it on (trash.go).
	_ plug.Trasher = (*docEngine)(nil)
	_ plug.Trasher = (*historied)(nil)
	_ plug.Trasher = (*sealedEngine)(nil)
	_ plug.Trasher = (*sqlEngine)(nil)

	_ plug.VersionWeighter = (*docEngine)(nil)
	_ plug.VersionWeighter = (*historied)(nil)
	_ plug.VersionWeighter = (*sealedEngine)(nil)
	_ plug.VersionWeighter = (*sqlEngine)(nil)

	_ sealingSource = (*TopicService[struct{}])(nil)
	_ topicWaker    = (*TopicService[struct{}])(nil)
	_ typeSource    = (*TopicService[struct{}])(nil)

	_ autoLoop  = (*WorkflowService[struct{}, string])(nil)
	_ journaled = (*WorkflowService[struct{}, string])(nil)
	_ starter   = (*WorkflowService[struct{}, string])(nil)
)
