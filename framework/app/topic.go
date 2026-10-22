package app

import (
	"time"

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

// Drain is how long the message a handler is running when the app stops may
// still run before its context is cancelled — a subscription's, a watch's, or
// a queued command's. A handler that finishes within it is acknowledged; one
// it cuts short is handed back uncounted, delivered again at the next start,
// and is no failure. The default is 5 seconds; zero cancels the handler at
// once, and a negative one stops the start. The app's stop gives each
// component the longest drain plus its settle ([Settle]), and tells every
// consumer at once, so that their drains overlap.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-drain,
// the kind subscription or command, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Drain(d time.Duration) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Drain(d)
}

// Settle bounds each outcome a consumer sends once the app began to stop —
// an acknowledgement, a nack, a message handed back — and each renewal of a
// lease ([AutoExtend]): a queue that stopped answering cannot hold the stop.
// The default is 5 seconds; one that is not positive stops the start.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-settle,
// which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Settle(d time.Duration) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Settle(d)
}

// Lease is how long a delivered message is held by its consumer before the
// queue hands it to another — a subscription's, a watch's, or a queued
// command's. The default is 30 seconds; one that is not positive stops the
// start. With [AutoExtend], a handler that runs longer keeps it.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-lease,
// which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Lease(d time.Duration) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Lease(d)
}

// AutoExtend says whether the lease of the message a handler is running is
// renewed, a third of the lease after it was granted or last renewed, for as
// long as the handler runs — a subscription's, a watch's, or a queued
// command's. It is on by default: a slow handler keeps its message instead of
// seeing it delivered to another while it runs. Off, the handler is stopped a
// margin before its [Lease] ends. A renewal the queue refuses cancels the
// handler's context. A mailer's outbox never renews: the mail spool parks a
// mail by its own lease.
//
// on is the default of the consumer's setting
// <service>-<kind>-<name>-auto-extend, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func AutoExtend(on bool) DeliveryOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.AutoExtend(on)
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
