package app

import "testing"

// A policy's setting is named after its service and its operation, each
// spelled as a setting's name.
func TestAPolicySettingIsNamedAfterItsOperation(t *testing.T) {
	for _, tc := range []struct{ service, kind, operation, want string }{
		{"shop", "endpoint", "CreateItem", "shop-endpoint-create-item-rate"},
		{"shop", "endpoint", "GET /items/{id}", "shop-endpoint-get-items-id-rate"},
		{"shop", "endpoint", "v2Handler", "shop-endpoint-v2-handler-rate"},
		{"shop", "query", "HTTPServer", "shop-query-httpserver-rate"},
		{"my_shop", "command", "place-order", "my-shop-command-place-order-rate"},
	} {
		if got := policySettingName(tc.service, tc.kind, tc.operation, knobRate); got != tc.want {
			t.Errorf("%s, %s: %q, want %q", tc.service, tc.operation, got, tc.want)
		}
	}
}
