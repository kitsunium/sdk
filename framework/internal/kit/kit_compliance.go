// Package kit — the compile-time proof of which roles each declaration plays:
// the interfaces the app reaches its nodes and options through.
package kit

import "github.com/kitsunium/sdk/pkg/v1/statemachine"

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

	_ starter = (*Loop)(nil)
	_ starter = (*Routine)(nil)
	_ starter = (*Secret)(nil)
	_ starter = (*Watch)(nil)
	_ starter = (*SubscriptionWorker[struct{}])(nil)

	_ mailbox = (*Mailer)(nil)
	_ starter = (*Mailer)(nil)

	_ settingDecl = (*SettingService[string])(nil)

	_ mounter = (*Frontend)(nil)

	_ Keeper          = (*StoreService[struct{}])(nil)
	_ formerSource    = (*StoreService[struct{}])(nil)
	_ itemsSource     = (*StoreService[struct{}])(nil)
	_ marking         = (*StoreService[struct{}])(nil)
	_ privacyStore    = (*StoreService[struct{}])(nil)
	_ retentionRunner = (*StoreService[struct{}])(nil)
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

	_ databaseSource = (*historied[struct{}])(nil)
	_ databaseSource = (*sealedEngine[struct{}])(nil)
	_ databaseSource = (*sqlEngine[struct{}])(nil)

	_ versionKeeper = (*docEngine[struct{}])(nil)
	_ versionKeeper = (*historied[struct{}])(nil)
	_ versionKeeper = (*sealedEngine[struct{}])(nil)
	_ versionKeeper = (*sqlEngine[struct{}])(nil)

	_ sealingSource = (*TopicService[struct{}])(nil)
	_ topicWaker    = (*TopicService[struct{}])(nil)
	_ typeSource    = (*TopicService[struct{}])(nil)

	_ autoLoop  = (*WorkflowService[struct{}, string])(nil)
	_ journaled = (*WorkflowService[struct{}, string])(nil)
	_ starter   = (*WorkflowService[struct{}, string])(nil)
)
