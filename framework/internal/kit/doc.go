//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/internal/kit .

// Package kit is the implementation of framework/kit, the framework in which
// every product is its own diagram. A product imports framework/kit, whose
// names alias and forward to this package's: here, the types carry the role
// their name says — StoreService, EndpointService, AppConfigurer — and the
// facade keeps the names a product writes — Store, Endpoint, AppOption.
package kit
