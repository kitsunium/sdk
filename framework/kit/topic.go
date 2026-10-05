package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Topic is an asynchronous channel of messages of type T. Every subscription
// receives every message, at least once: a subscription's handler must
// tolerate a redelivery.
type Topic[T any] = ikit.TopicService[T]

// Subscription consumes a topic: its handler runs once per message, at least
// once, retried on error, and dead-lettered after MaxDeliveries attempts.
type Subscription[T any] = ikit.SubscriptionWorker[T]

// SubscriptionOption configures a subscription.
type SubscriptionOption = ikit.SubscriptionConfigurer

// DeliveryOption configures a queue's deliveries: a subscription's, or a
// queued command's ([MaxDeliveries], [Parallelism]).
type DeliveryOption = ikit.DeliveryOption

// maxDeliveries is MaxDeliveries's body: decl_gen.go writes MaxDeliveries, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func maxDeliveries(n int) DeliveryOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MaxDeliveries(n)
}

// parallelism is Parallelism's body: decl_gen.go writes Parallelism, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func parallelism(n int) DeliveryOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Parallelism(n)
}

// ownStores is OwnStores's body: decl_gen.go writes OwnStores, from the
// design, as one call of it.
func ownStores() SubscriptionOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.OwnStores()
}
