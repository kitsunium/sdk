package app

import "testing"

// A policy's setting is named after its service and its operation, each
// spelled as a setting's name.
func TestAPolicySettingIsNamedAfterItsOperation(t *testing.T) {
	for _, tc := range []struct{ service, operation, want string }{
		{"shop", "CreateItem", "shop-create-item-rate"},
		{"shop", "GET /items/{id}", "shop-get-items-id-rate"},
		{"shop", "v2Handler", "shop-v2-handler-rate"},
		{"shop", "HTTPServer", "shop-httpserver-rate"},
		{"my_shop", "place-order", "my-shop-place-order-rate"},
	} {
		if got := policySettingName(tc.service, tc.operation, knobRate); got != tc.want {
			t.Errorf("%s, %s: %q, want %q", tc.service, tc.operation, got, tc.want)
		}
	}
}
