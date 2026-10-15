package kit

import (
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

	_ storeEngine[struct{}] = (*docEngine[struct{}])(nil)

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

	_ documenter = (*historied[struct{}])(nil)
	_ documenter = (*sealedEngine[struct{}])(nil)
	_ documenter = (*docEngine[struct{}])(nil)
	_ documenter = (*sqlEngine[struct{}])(nil)
	_ copyDocs   = (*docEngine[struct{}])(nil)
	_ copySink   = (*sqlEngine[struct{}])(nil)
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
	_ sealAller       = (*StoreService[struct{}])(nil)
	_ tabled          = (*StoreService[struct{}])(nil)

	_ folder = (*docEngine[struct{}])(nil)
	_ folder = (*historied[struct{}])(nil)
	_ folder = (*sealedEngine[struct{}])(nil)

	_ entriesSource = (*docEngine[struct{}])(nil)
	_ entriesSource = (*sqlEngine[struct{}])(nil)

	_ keyser = (*docEngine[struct{}])(nil)
	_ keyser = (*sqlEngine[struct{}])(nil)
	_ keyser = (*sealedEngine[struct{}])(nil)
	_ keyser = (*historied[struct{}])(nil)

	_ databaseSource = (*historied[struct{}])(nil)
	_ databaseSource = (*sealedEngine[struct{}])(nil)
	_ databaseSource = (*sqlEngine[struct{}])(nil)

	_ versionKeeper = (*docEngine[struct{}])(nil)
	_ versionKeeper = (*historied[struct{}])(nil)
	_ versionKeeper = (*sealedEngine[struct{}])(nil)
	_ versionKeeper = (*sqlEngine[struct{}])(nil)

	// The engines that keep a trash, and those that hand it on (trash.go).
	_ trasher = (*docEngine[struct{}])(nil)
	_ trasher = (*historied[struct{}])(nil)
	_ trasher = (*sealedEngine[struct{}])(nil)
	_ trasher = (*sqlEngine[struct{}])(nil)

	_ sealingSource = (*TopicService[struct{}])(nil)
	_ topicWaker    = (*TopicService[struct{}])(nil)
	_ typeSource    = (*TopicService[struct{}])(nil)

	_ autoLoop  = (*WorkflowService[struct{}, string])(nil)
	_ journaled = (*WorkflowService[struct{}, string])(nil)
	_ starter   = (*WorkflowService[struct{}, string])(nil)
)
