package app

// : Asserts at compile time that every consumer — a subscription, a watch, a
// queued command — is told of the app's stop before its component stops.
var (
	_ drainer = (*SubscriptionWorker[any])(nil)
	_ drainer = (*Watch)(nil)
	_ drainer = (*Command[any, any])(nil)
)
