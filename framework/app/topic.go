package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Topic is an asynchronous channel of messages of type T. Every subscription
// receives every message, at least once: a subscription's handler must
// tolerate a redelivery.
type Topic[T any] = iapp.TopicService[T]

// Subscription consumes a topic: its handler runs once per message, at least
// once, retried on error, and dead-lettered after MaxDeliveries attempts.
type Subscription[T any] = iapp.SubscriptionWorker[T]

// SubscriptionOption configures a subscription.
type SubscriptionOption = iapp.SubscriptionConfigurer

// DeliveryOption configures a queue's deliveries: a subscription's, or a
// queued command's ([MaxDeliveries], [Parallelism]).
type DeliveryOption = iapp.DeliveryOption

// MaxDeliveries is how many attempts a message gets before it is
// dead-lettered — a subscription's, or a queued command's. The default is
// 5.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MaxDeliveries(n int) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.MaxDeliveries(n)
}

// Parallelism is how many messages are handled at once — a subscription's,
// or a queued command's. The default is 1.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Parallelism(n int) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Parallelism(n)
}

// OwnStores lets a watch ([Service].Watch) hear the stores of its own
// module too — the product's own, for a watch of the product's —, which a
// watch never hears by default. Such a watch never hears the writes its own
// handler makes, in its handler's context — a handler that writes what it
// watches, a screening that stamps the record it screened, would otherwise
// hear itself forever —; a write the handler only causes, later and
// elsewhere — a queued command's handling, a topic's delivery — is heard.
// A subscription to a topic refuses it: a topic has no stores.
func OwnStores() SubscriptionOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OwnStores()
}
