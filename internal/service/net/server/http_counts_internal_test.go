package server

import (
	"testing"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// TestNetHTTPErrorType holds the classification of net/http's CountError names
// (#966): a scope and an RFC code, a frame layer name by its code, the lost
// PING, and anything else as other.
func TestNetHTTPErrorType(t *testing.T) {
	cases := []struct {
		in   string
		want corehttp2.ErrorType
	}{
		{"conn_PROTOCOL_ERROR_first_settings", corehttp2.ErrorTypeProtocol},
		{"stream_FLOW_CONTROL_ERROR_data_flow", corehttp2.ErrorTypeFlowControl},
		{"stream_REFUSED_STREAM_over_max_streams_race", corehttp2.ErrorTypeRefusedStream},
		{"conn_ENHANCE_YOUR_CALM_too_many_early_resets", corehttp2.ErrorTypeEnhanceYourCalm},
		{"stream_STREAM_CLOSED_closed", corehttp2.ErrorTypeStreamClosed},
		{"conn_close_lost_ping", corehttp2.ErrorTypePingTimeout},
		{"frame_settings_mod_6", corehttp2.ErrorTypeFrameSize},
		{"frame_ping_length", corehttp2.ErrorTypeFrameSize},
		{"frame_settings_window_size_too_big", corehttp2.ErrorTypeFlowControl},
		{"frame_data_stream_0", corehttp2.ErrorTypeProtocol},
		{"conn_153_mystery", corehttp2.ErrorTypeOther},
		{"something_new", corehttp2.ErrorTypeOther},
		{"", corehttp2.ErrorTypeOther},
	}
	for _, tc := range cases {
		if got := netHTTPErrorType(tc.in); got != tc.want {
			t.Errorf("netHTTPErrorType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
