package kit_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// A watch that hears its own module's stores (ADR 0008, amended
// 2026-09-29): kit.OwnStores. The atelier screens its own sketches — a
// watch of the product's, on the product's store — and stamps each sketch
// it screened: a write of the store it hears, which it never hears back. A
// watch without the option still hears none of the product's stores.

// Atelier keeps sketches, and screens them itself.
var Atelier = kit.NewService("atelier", "Sketches, screened by the atelier itself.")

// Sketch is one sketch: its caption is moderated, and the screening counts
// its passes.
type Sketch struct {
	ID       string `json:"id"`
	Caption  string `json:"caption" kit:"moderated"`
	Screened int    `json:"screened"`
}

var (
	Sketches = Atelier.Store("sketches", func(s Sketch) string { return s.ID })
	// ScreenSketches hears the atelier's own sketches, and stamps them.
	ScreenSketches = Atelier.Watch("screen", kit.Moderated, screenSketch, kit.OwnStores())
	// BlindWatch hears the modules' stores only: the atelier mounts none.
	BlindWatch = Atelier.Watch("blind", kit.Moderated, func(context.Context, kit.WrittenEvent) error {
		atelier.Lock()
		defer atelier.Unlock()
		atelier.blind++
		return nil
	})
)

// atelier is what the watches heard: the screening's notices, the passes
// it finished, and how often the blind watch was told anything.
var atelier struct {
	sync.Mutex
	screened []kit.WrittenEvent
	passes   int
	blind    int
}

func resetAtelier() {
	atelier.Lock()
	defer atelier.Unlock()
	atelier.screened, atelier.passes, atelier.blind = nil, 0, 0
}

// screenSketch stamps the sketch a notice names, in the context kit hands
// it: a write of a store it hears, its own.
func screenSketch(ctx context.Context, w kit.WrittenEvent) error {
	atelier.Lock()
	atelier.screened = append(atelier.screened, w)
	atelier.Unlock()
	if w.Deleted {
		return nil
	}
	if _, err := Sketches.Update(ctx, w.Key, func(s *Sketch) error { s.Screened++; return nil }); err != nil {
		return err
	}
	atelier.Lock()
	atelier.passes++
	atelier.Unlock()
	return nil
}

// screenedOf are the notices the screening heard of one sketch.
func screenedOf(key string) []kit.WrittenEvent {
	atelier.Lock()
	defer atelier.Unlock()
	return slices.DeleteFunc(slices.Clone(atelier.screened), func(w kit.WrittenEvent) bool { return w.Key != key })
}

// A watch given kit.OwnStores hears the product's own store — a write,
// once —, never the write its own handler makes there, which would
// otherwise tell it again, forever; a watch without it hears nothing of
// the product's stores.
func TestAWatchHearsItsOwnStoresNeverItsOwnWrites(t *testing.T) {
	resetAtelier()
	startApp(t, []*kit.Service{Atelier})
	ctx := t.Context()
	must(t, Sketches.Insert(ctx, Sketch{ID: "cat", Caption: "a cat"}))
	eventually(t, "the screening's pass", func() bool {
		atelier.Lock()
		defer atelier.Unlock()
		return atelier.passes >= 1
	})
	// The pass's own write, had it been told, was queued before this one:
	// once this is heard, every notice before it has been.
	must(t, Sketches.Insert(ctx, Sketch{ID: "dog", Caption: "a dog"}))
	eventually(t, "the second sketch", func() bool { return len(screenedOf("dog")) > 0 })
	switch got := screenedOf("cat"); {
	case len(got) != 1:
		t.Errorf("the screening heard of the cat %d times, its own stamps included", len(got))
	case got[0].Store != "atelier/store/sketches" || !slices.Equal(got[0].Fields, []kit.FieldRefValue{{Store: "atelier/store/sketches", Path: "/caption"}}):
		t.Errorf("the screening heard of the cat: %+v", got[0])
	}
	if s, err := Sketches.Get(ctx, "cat"); err != nil || s.Screened != 1 {
		t.Errorf("the cat was screened %d times: %v", s.Screened, err)
	}
	atelier.Lock()
	blind := atelier.blind
	atelier.Unlock()
	if blind != 0 {
		t.Errorf("a watch without OwnStores heard the product's store %d times", blind)
	}
}

// The graph says a watch hears its own module's stores, and draws the
// store that feeds it; a watch without the option draws none.
func TestTheGraphSaysAWatchHearsItsOwnStores(t *testing.T) {
	g := startApp(t, []*kit.Service{Atelier}).Graph()
	screen := g.Node("atelier/subscription/screen").Subscription
	if screen == nil || !screen.OwnStores || !slices.Equal(screen.Stores, []string{"atelier/store/sketches"}) {
		t.Fatalf("the screening: %+v", screen)
	}
	if feeds := storesDelivering(g, "atelier/subscription/screen"); !slices.Equal(feeds, []string{"atelier/store/sketches"}) {
		t.Errorf("the declared edges feed the screening from %v", feeds)
	}
	if blind := g.Node("atelier/subscription/blind").Subscription; blind == nil || blind.OwnStores || len(blind.Stores) > 0 {
		t.Errorf("the blind watch: %+v", blind)
	}
}

// A module's watch given kit.OwnStores hears its own module's stores as
// well as the product's; one without hears the product's alone.
func TestAModulesWatchHearsItsOwnModuleWhenAsked(t *testing.T) {
	type card struct {
		ID   string `json:"id"`
		Text string `json:"text" kit:"moderated"`
	}
	desk := kit.NewService("desk", "The pinboard's desk.")
	desk.Store("cards", func(c card) string { return c.ID })
	desk.Watch("all", kit.Moderated, func(context.Context, kit.WrittenEvent) error { return nil }, kit.OwnStores())
	desk.Watch("others", kit.Moderated, func(context.Context, kit.WrittenEvent) error { return nil })
	pinboard := kit.NewModule("pinboard", "Cards, screened with the product's sketches.", desk)
	g := startApp(t, []*kit.Service{Atelier}, kit.Mount(pinboard)).Graph()
	for id, want := range map[string][]string{
		"pinboard.desk/subscription/all":    {"atelier/store/sketches", "pinboard.desk/store/cards"},
		"pinboard.desk/subscription/others": {"atelier/store/sketches"},
	} {
		if feeds := storesDelivering(g, id); !slices.Equal(feeds, want) {
			t.Errorf("%s is fed by %v, want %v", id, feeds, want)
		}
	}
}

// A subscription to a topic refuses kit.OwnStores: a topic has no stores.
func TestOwnStoresIsAWatchsAlone(t *testing.T) {
	desk := kit.NewService("own-topic", "Subscribes wrongly.")
	topic := desk.Topic[string]("news")
	desk.Subscribe("reader", topic, func(context.Context, string) error { return nil }, kit.OwnStores())
	de := startRefused(t, kit.NewApp("own-topic", desk))
	if !slices.ContainsFunc(de.Diagnostics, func(d model.Diagnostic) bool {
		return strings.Contains(d.Message, `subscription "reader" is given kit.OwnStores, which is a watch's`)
	}) {
		t.Errorf("no problem says it: %+v", de.Diagnostics)
	}
}
