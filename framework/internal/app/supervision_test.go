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

// supervisionKnobs are the settings supervisedService declares: each
// consumer's, each loop's restart intensity and each job's overlap and
// starting deadline, with the code's value and another a configuration
// gives.
var supervisionKnobs = []policyKnob{
	{"jobs-subscription-index-give-up-restarts", "SHOP_JOBS_SUBSCRIPTION_INDEX_GIVE_UP_RESTARTS", "int", "3", "5"},
	{"jobs-subscription-index-give-up-within", "SHOP_JOBS_SUBSCRIPTION_INDEX_GIVE_UP_WITHIN", "duration", "1m0s", "2m0s"},
	{"jobs-subscription-screen-give-up-restarts", "SHOP_JOBS_SUBSCRIPTION_SCREEN_GIVE_UP_RESTARTS", "int", "10", "4"},
	{"jobs-subscription-screen-give-up-within", "SHOP_JOBS_SUBSCRIPTION_SCREEN_GIVE_UP_WITHIN", "duration", "10m0s", "0s"},
	{"jobs-command-reindex-give-up-restarts", "SHOP_JOBS_COMMAND_REINDEX_GIVE_UP_RESTARTS", "int", "10", "1"},
	{"jobs-command-reindex-give-up-within", "SHOP_JOBS_COMMAND_REINDEX_GIVE_UP_WITHIN", "duration", "10m0s", "30m0s"},
	{"jobs-loop-tick-give-up-restarts", "SHOP_JOBS_LOOP_TICK_GIVE_UP_RESTARTS", "int", "2", "6"},
	{"jobs-loop-tick-give-up-within", "SHOP_JOBS_LOOP_TICK_GIVE_UP_WITHIN", "duration", "1h0m0s", "3h0m0s"},
	{"jobs-loop-sweep-give-up-restarts", "SHOP_JOBS_LOOP_SWEEP_GIVE_UP_RESTARTS", "int", "10", "7"},
	{"jobs-loop-sweep-give-up-within", "SHOP_JOBS_LOOP_SWEEP_GIVE_UP_WITHIN", "duration", "10m0s", "20m0s"},
	{"jobs-job-tally-overlap", "SHOP_JOBS_JOB_TALLY_OVERLAP", "text", "replace", "allow"},
	{"jobs-job-tally-starting-deadline", "SHOP_JOBS_JOB_TALLY_STARTING_DEADLINE", "duration", "30s", "1m0s"},
	{"jobs-job-digest-overlap", "SHOP_JOBS_JOB_DIGEST_OVERLAP", "text", "skip", "replace"},
	{"jobs-job-digest-starting-deadline", "SHOP_JOBS_JOB_DIGEST_STARTING_DEADLINE", "duration", "0s", "5m0s"},
}

// supervisedService is a service with what the framework supervises, each
// kind — a subscription whose code names its intensity, a watch, a queued
// command, a declared and a hand-written loop on the defaults or not — and
// two jobs, one whose code names its overlap and deadline, one on the
// defaults, and a command that is not queued.
func supervisedService() *app.Service {
	s := app.NewService("jobs", "What the framework supervises, each kind.")
	topic := s.Topic[event]("events")
	s.Subscribe("index", topic, func(context.Context, event) error { return nil }, app.GiveUpAfter(3, time.Minute))
	s.Watch("screen", app.Moderated, func(context.Context, app.WrittenEvent) error { return nil })
	s.Command("reindex", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.Queued())
	s.Command("now", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil })
	s.Loop("tick", func(context.Context, app.WakeEvent) error { return nil }, app.WakeEvery(time.Hour), app.GiveUpAfter(2, time.Hour))
	s.Go("sweep", func(ctx context.Context) error { <-ctx.Done(); return nil })
	s.Every("tally", time.Hour, func(context.Context) error { return nil }, app.Overlap(app.OverlapReplace), app.StartingDeadline(30*time.Second))
	s.Cron("digest", "0 9 * * *", func(context.Context) error { return nil })
	return s
}

// What the framework supervises declares its restart intensity in its
// service, and a job its overlap and starting deadline, the code's value
// their default — ten restarts within ten minutes, a skipped overlap, no
// deadline —; a command that is not queued declares none. The graph says
// when a loop gives up.
func TestTheSupervisionsSettingsDefaultToTheCode(t *testing.T) {
	a, err := startPolicies(t, []*app.Service{supervisedService()})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range supervisionKnobs {
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
	g := a.Graph()
	for node, want := range map[string]string{
		"jobs/loop/tick":  "gives up after 2 within 1h0m0s",
		"jobs/loop/sweep": "gives up after 10 within 10m0s",
	} {
		if n := g.Node(node); n == nil || n.Loop == nil || !strings.Contains(n.Loop.Restart, want) {
			t.Errorf("%s draws its restart as %+v, want %q", node, n, want)
		}
	}
}

// A configuration file overrides each of these settings, and the variable
// overrides the file: the configuration says where each value came from.
func TestTheSupervisionsSettingsAreOverriddenByTheConfiguration(t *testing.T) {
	var file strings.Builder
	file.WriteString("{")
	for i, k := range supervisionKnobs {
		if i > 0 {
			file.WriteString(",")
		}
		value := k.other
		if k.kind == "duration" || k.kind == "text" {
			value = `"` + value + `"`
		}
		file.WriteString(`"` + k.key + `":` + value)
	}
	file.WriteString("}")
	t.Setenv("SHOP_JOBS_LOOP_TICK_GIVE_UP_RESTARTS", "9")
	a, err := startPolicies(t, []*app.Service{supervisedService()},
		app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(file.String())}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range supervisionKnobs {
		t.Run(k.key, func(t *testing.T) {
			s := settingOf(t, a, k.variable)
			want := model.Setting{From: model.SettingFile, Detail: "config/dev.json", Value: k.other}
			if k.variable == "SHOP_JOBS_LOOP_TICK_GIVE_UP_RESTARTS" {
				want = model.Setting{From: model.SettingEnv, Value: "9"}
			}
			if s.From != want.From || s.Detail != want.Detail || s.Value != want.Value {
				t.Errorf("%+v, want %+v", s, want)
			}
		})
	}
	g := a.Graph()
	if n := g.Node("jobs/loop/tick"); n == nil || !strings.Contains(n.Loop.Restart, "gives up after 9 within 3h0m0s") {
		t.Errorf("the declared loop draws %+v", n)
	}
	for _, m := range g.Node("jobs/command/reindex").Command.Pipeline {
		if m.Kind == "queue" && m.Config["giveUp"] != "gives up after 1 within 30m0s" {
			t.Errorf("the queued command's queue draws %v", m.Config)
		}
	}
}

// A key nothing declares is refused: a command that is not queued, a job's
// intensity — a job is not restarted —, a loop's overlap, a knob misspelled.
func TestAKeyNoSupervisionDeclaresIsRefused(t *testing.T) {
	for _, stray := range []string{
		"jobs-command-now-give-up-restarts", "jobs-job-tally-give-up-within", "jobs-loop-tick-overlap",
		"jobs-loop-sweep-give-up", "jobs-job-digest-deadline",
	} {
		t.Run(stray, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{supervisedService()},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + stray + `": "1s"}`)}}))
			if err == nil || !strings.Contains(err.Error(), `config/config.json sets "`+stray+`", which no service declares`) {
				t.Errorf("%s: %v", stray, err)
			}
		})
	}
}

// A value the framework cannot run with stops the start, from the
// configuration and from the code alike: a negative intensity or span, a
// word that names no overlap policy, a negative deadline.
func TestASupervisionValueItCannotRunWithStopsTheStart(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"jobs-subscription-index-give-up-restarts", `-1`},
		{"jobs-loop-tick-give-up-within", `"-1s"`},
		{"jobs-loop-sweep-give-up-restarts", `-3`},
		{"jobs-command-reindex-give-up-within", `"-1m"`},
		{"jobs-job-tally-overlap", `"sometimes"`},
		{"jobs-job-digest-starting-deadline", `"-5s"`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{supervisedService()},
				app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"` + tc.key + `": ` + tc.value + `}`)}}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Errorf("%s = %s: %v", tc.key, tc.value, err)
			}
		})
	}
	s := app.NewService("jobs", "Values the code gives that nothing runs with.")
	s.Go("sweep", func(ctx context.Context) error { <-ctx.Done(); return nil }, app.GiveUpAfter(-1, time.Minute))
	s.Every("tally", time.Hour, func(context.Context) error { return nil }, app.Overlap("never"))
	_, err := startPolicies(t, []*app.Service{s})
	if err == nil || !strings.Contains(err.Error(), `setting "jobs-loop-sweep-give-up-restarts"`) ||
		!strings.Contains(err.Error(), `setting "jobs-job-tally-overlap"`) {
		t.Errorf("an intensity and an overlap the code gives: %v", err)
	}
}

// A command that is not queued refuses GiveUpAfter, as it refuses every
// consumer's option: it has no consumer to restart.
func TestACommandNotQueuedRefusesGiveUpAfter(t *testing.T) {
	s := app.NewService("jobs", "An intensity on a command that is not queued.")
	s.Command("now", func(context.Context, event) (app.EmptyValue, error) { return app.EmptyValue{}, nil }, app.GiveUpAfter(1, time.Minute))
	if _, err := startPolicies(t, []*app.Service{s}); err == nil || !strings.Contains(err.Error(), "jobs/command/now") {
		t.Errorf("an intensity on a command that is not queued: %v", err)
	}
}
