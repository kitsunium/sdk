// Package kit — topics and subscriptions: asynchronous messages of one type.
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

// MaxDeliveries is how many attempts a message gets before it is
// dead-lettered — a subscription's, or a queued command's. The default is
// 5.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MaxDeliveries(n int) DeliveryOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MaxDeliveries(n)
}

// Parallelism is how many messages are handled at once — a subscription's,
// or a queued command's. The default is 1.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Parallelism(n int) DeliveryOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Parallelism(n)
}
