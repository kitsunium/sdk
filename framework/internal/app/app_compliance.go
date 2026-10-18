package app

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
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

	_ plug.StoreEngine = (*sealedEngine)(nil)
	_ plug.StoreEngine = (*historied)(nil)

	_ plug.WorkflowStore   = workflowPort[struct{}, string]{}
	_ plug.WorkflowJournal = (*workflowJournal)(nil)
	_ plug.WorkflowJournal = (*sqlJournal)(nil)

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

	_ plug.Clock = pluggedClock{}

	_ plug.JobScheduler = idleScheduler{}

	_ plug.DocumentSource = (*historied)(nil)
	_ plug.DocumentSource = (*sealedEngine)(nil)
	_ copyDocs            = (*jsonDocs)(nil)

	_ plug.Transaction = kitTransaction{}

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

	// A store's roles are its core's (store_core.go, ADR 0179 §2): the
	// typed facade above has them by embedding it.
	_ Keeper          = (*storeCore)(nil)
	_ copyPartser     = (*storeCore)(nil)
	_ feeder          = (*storeCore)(nil)
	_ formerSource    = (*storeCore)(nil)
	_ itemsSource     = (*storeCore)(nil)
	_ marking         = (*storeCore)(nil)
	_ node            = (*storeCore)(nil)
	_ placementSource = (*storeCore)(nil)
	_ recordsPort     = (*storeCore)(nil)
	_ retentionRunner = (*storeCore)(nil)
	_ revisionsSource = (*storeCore)(nil)
	_ sealAller       = (*storeCore)(nil)
	_ sealingSource   = (*storeCore)(nil)
	_ starter         = (*storeCore)(nil)
	_ tabled          = (*storeCore)(nil)
	_ trashRunner     = (*storeCore)(nil)
	_ trashSource     = (*storeCore)(nil)
	_ typeSource      = (*storeCore)(nil)

	_ plug.Folder = (*historied)(nil)
	_ plug.Folder = (*sealedEngine)(nil)

	_ plug.Keyser = (*sealedEngine)(nil)
	_ plug.Keyser = (*historied)(nil)

	_ plug.VersionKeeper = (*historied)(nil)
	_ plug.VersionKeeper = (*sealedEngine)(nil)

	// The engines that keep a trash, and those that hand it on (trash.go).
	_ plug.Trasher = (*historied)(nil)
	_ plug.Trasher = (*sealedEngine)(nil)

	_ plug.VersionWeighter = (*historied)(nil)
	_ plug.VersionWeighter = (*sealedEngine)(nil)

	_ sealingSource = (*TopicService[struct{}])(nil)
	_ topicWaker    = (*TopicService[struct{}])(nil)
	_ typeSource    = (*TopicService[struct{}])(nil)

	_ autoLoop  = (*WorkflowService[struct{}, string])(nil)
	_ journaled = (*WorkflowService[struct{}, string])(nil)
	_ starter   = (*WorkflowService[struct{}, string])(nil)
)
