package statemachine_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTriggersSpellAndParseBack pins the closed set: five names, each parsed
// back to its trigger, and nothing else — the zero value has no name and an
// unknown name is refused.
func TestTriggersSpellAndParseBack(t *testing.T) {
	t.Parallel()
	want := map[corestm.Trigger]string{
		corestm.TriggerStart: "start", corestm.TriggerEvent: "event", corestm.TriggerDelay: "delay",
		corestm.TriggerDeadline: "deadline", corestm.TriggerGuard: "guard",
	}
	for trigger, name := range want {
		if trigger.String() != name {
			t.Errorf("%d.String() = %q; want %q", trigger, trigger.String(), name)
		}
		parsed, err := corestm.ParseTrigger(name)
		if err != nil || parsed != trigger {
			t.Errorf("ParseTrigger(%q) = %v, %v", name, parsed, err)
		}
	}
	if s := corestm.Trigger(0).String(); s != "" {
		t.Errorf("the zero trigger is named %q", s)
	}
	if s := corestm.Trigger(99).String(); s != "" {
		t.Errorf("a trigger outside the set is named %q", s)
	}
	for _, name := range []string{"", "Start", "timer", "create"} {
		if _, err := corestm.ParseTrigger(name); !errs.HasCode(err, corestm.CodeTriggerUnknown) {
			t.Errorf("ParseTrigger(%q) = %v; want TRIGGER_UNKNOWN", name, err)
		}
	}
}

// TestARecordRoundTripsThroughJSON pins that a record survives the encoding a
// file journal would use — the trigger as its stable number.
func TestARecordRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	rec := corestm.RecordValue[string]{Key: "o-1", State: "paid", Entered: at, History: []corestm.StepValue[string]{
		{Event: corestm.CreateEvent, To: "pending", Trigger: corestm.TriggerStart, Actor: "api", At: at.Add(-time.Hour)},
		{Event: "pay", From: "pending", To: "paid", Trigger: corestm.TriggerEvent, At: at},
	}}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back corestm.RecordValue[string]
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rec) {
		t.Fatalf("round trip:\n%+v\nwant\n%+v", back, rec)
	}
}
