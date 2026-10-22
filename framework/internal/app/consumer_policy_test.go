package app_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// consumerKnobs are the settings consumerService's consumers declare: a
// subscription's, a watch's — a subscription's kind — and a queued
// command's, and its topic's window; each knob with the code's value and
// another a configuration gives.
var consumerKnobs = []policyKnob{
	{"jobs-subscription-index-drain", "SHOP_JOBS_SUBSCRIPTION_INDEX_DRAIN", "duration", "2s", "3s"},
	{"jobs-subscription-index-settle", "SHOP_JOBS_SUBSCRIPTION_INDEX_SETTLE", "duration", "4s", "6s"},
	{"jobs-subscription-index-lease", "SHOP_JOBS_SUBSCRIPTION_INDEX_LEASE", "duration", "1m0s", "2m0s"},
	{"jobs-subscription-index-auto-extend", "SHOP_JOBS_SUBSCRIPTION_INDEX_AUTO_EXTEND", "bool", "false", "true"},
	{"jobs-subscription-screen-drain", "SHOP_JOBS_SUBSCRIPTION_SCREEN_DRAIN", "duration", "5s", "7s"},
	{"jobs-subscription-screen-settle", "SHOP_JOBS_SUBSCRIPTION_SCREEN_SETTLE", "duration", "5s", "8s"},
	{"jobs-subscription-screen-lease", "SHOP_JOBS_SUBSCRIPTION_SCREEN_LEASE", "duration", "30s", "45s"},
	{"jobs-subscription-screen-auto-extend", "SHOP_JOBS_SUBSCRIPTION_SCREEN_AUTO_EXTEND", "bool", "true", "false"},
	{"jobs-command-reindex-drain", "SHOP_JOBS_COMMAND_REINDEX_DRAIN", "duration", "0s", "1s"},
	{"jobs-command-reindex-settle", "SHOP_JOBS_COMMAND_REINDEX_SETTLE", "duration", "5s", "9s"},
	{"jobs-command-reindex-lease", "SHOP_JOBS_COMMAND_REINDEX_LEASE", "duration", "30s", "1m30s"},
	{"jobs-command-reindex-auto-extend", "SHOP_JOBS_COMMAND_REINDEX_AUTO_EXTEND", "bool", "true", "false"},
	{"jobs-topic-events-once-window", "SHOP_JOBS_TOPIC_EVENTS_ONCE_WINDOW", "duration", "1m0s", "2m0s"},
}

// event is a topic's message in consumerService.
type event struct {
	ID string `json:"id"`
}

// consumerService is a service with a consumer of each kind: a subscription
// whose code names every knob, of a topic that publishes once per key, a
// watch on the defaults, a queued command that does not drain, and a command
// that is not queued.
func consumerService() *app.Service {
	s := app.NewService("jobs", "Consumers, each kind.")
	topic := s.Topic[event]("events").Once(func(e event) string { return e.ID }, time.Minute)
	s.Subscribe("index", topic, func(context.Context, event) error { return nil },
		app.Drain(2*time.Second), app.Settle(4*time.Second), app.Lease(time.Minute), app.AutoExtend(false))
	s.Watch("screen", app.Moderated, func(context.Context, app.WrittenEvent) error { return nil })
	s.Command("reindex", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.Queued(), app.Drain(0))
	s.Command("now", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil })
	return s
}

// A consumer declares its stop and its lease in its service, the code's
// value their default — DrainTimeout, SettleTimeout, the lease and
// AutoExtend, on by default —; a command that is not queued declares none.
func TestAConsumersSettingsDefaultToTheCode(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{consumerService()})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range consumerKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			if s.Key != k.key || s.Service != "jobs" || s.Type != k.kind || s.From != model.SettingDefault || s.Value != k.def {
				t.Errorf("%+v, want the key %s, a %s from the code: %s", s, k.key, k.kind, k.def)
			}
		})
	}
	for _, s := range a.Graph().Runtime.Config {
		if strings.HasPrefix(s.Key, "jobs-command-now-") {
			t.Errorf("a command that is not queued declares %s", s.Key)
		}
	}
	queue := map[string]string{}
	for _, m := range a.Graph().Node("jobs/command/reindex").Command.Pipeline {
		if m.Kind == "queue" {
			queue = m.Config
		}
	}
	if queue["drain"] != "0s" || queue["settle"] != "5s" || queue["lease"] != "30s" || queue["autoExtend"] != "true" {
		t.Errorf("the queued command's queue draws %v", queue)
	}
}

// A configuration file overrides each setting of a consumer, and the
// variable overrides the file: the configuration says where each value came
// from.
func TestAConsumersSettingsAreOverriddenByTheConfiguration(t *testing.T) {
	var file strings.Builder
	file.WriteString("{")
	for i, k := range consumerKnobs {
		if i > 0 {
			file.WriteString(",")
		}
		value := k.other
		if k.kind == "duration" {
			value = `"` + value + `"`
		}
		file.WriteString(`"` + k.key + `":` + value)
	}
	file.WriteString("}")
	t.Setenv("SHOP_JOBS_COMMAND_REINDEX_DRAIN", "12s")
	a, err := startPolicies(t, []*app.Service{consumerService()},
		app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(file.String())}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range consumerKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			want := model.Setting{From: model.SettingFile, Detail: "config/dev.json", Value: k.other}
			if k.variable == "SHOP_JOBS_COMMAND_REINDEX_DRAIN" {
				want = model.Setting{From: model.SettingEnv, Value: "12s"}
			}
			if s.From != want.From || s.Detail != want.Detail || s.Value != want.Value {
				t.Errorf("%+v, want %+v", s, want)
			}
		})
	}
	for _, m := range a.Graph().Node("jobs/command/reindex").Command.Pipeline {
		if m.Kind == "queue" && (m.Config["drain"] != "12s" || m.Config["lease"] != "1m30s" || m.Config["autoExtend"] != "false") {
			t.Errorf("the queued command's queue draws %v", m.Config)
		}
	}
}

// A key no consumer declares is refused: a consumer that does not exist, a
// command that is not queued, a mailer — whose outbox is the mail spool's —,
// or a knob misspelled.
func TestAKeyNoConsumerDeclaresIsRefused(t *testing.T) {
	for _, stray := range []string{
		"jobs-subscription-missing-drain", "jobs-command-now-drain", "jobs-command-now-auto-extend",
		"jobs-mailer-notify-auto-extend", "jobs-subscription-index-drains", "jobs-command-index-lease",
		"jobs-topic-others-once-window", "jobs-topic-events-window",
	} {
		t.Run(stray, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{consumerService()},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + stray + `": "1s"}`)}}))
			if err == nil || !strings.Contains(err.Error(), `config/config.json sets "`+stray+`", which no service declares`) {
				t.Errorf("%s: %v", stray, err)
			}
		})
	}
}

// A value a consumer cannot run with stops the start, from the
// configuration and from the code alike.
func TestAConsumerValueItCannotRunWithStopsTheStart(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"jobs-subscription-index-drain", `"-1s"`},
		{"jobs-subscription-screen-settle", `"0s"`},
		{"jobs-command-reindex-lease", `"0s"`},
		{"jobs-command-reindex-settle", `"-2s"`},
		{"jobs-subscription-index-auto-extend", `"maybe"`},
		{"jobs-topic-events-once-window", `"0s"`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{consumerService()},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": ` + tc.value + `}`)}}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Errorf("%s = %s: %v", tc.key, tc.value, err)
			}
		})
	}
	s := app.NewService("jobs", "A lease of zero.")
	topic := s.Topic[event]("events")
	s.Subscribe("index", topic, func(context.Context, event) error { return nil }, app.Lease(0))
	if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), `setting "jobs-subscription-index-lease"`) {
		t.Errorf("a lease of zero in the code: %v", err)
	}
}

// A command that is not queued refuses a consumer's option, as it refuses
// MaxDeliveries: it has no queue.
func TestACommandNotQueuedRefusesAConsumersOption(t *testing.T) {
	s := app.NewService("jobs", "A drain on a command that is not queued.")
	s.Command("now", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.Drain(time.Second))
	if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), "jobs/command/now") {
		t.Errorf("a drain on a command that is not queued: %v", err)
	}
}
