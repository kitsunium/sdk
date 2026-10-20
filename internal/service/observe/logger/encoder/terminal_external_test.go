package encoder_test

import (
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// terminalControls are what a console sink's terminal acts on, or what a
// viewer breaks a line at, besides CR, LF and NUL: ESC, the other C0
// controls, DEL, the C1 controls (NEL, and CSI which some terminals take for
// ESC [) and the two Unicode separators.
var terminalControls = []string{"\x1b", "\x07", "\x08", "\x09", "\x0b", "\x0c", "\x7f", "\u0085", "\u009b", " ", " "}

// TestTextEncoderNeutralisesTerminalControls pins #464 (CWE-150, CWE-117): a
// message, a group name and an attribute key carrying ESC [2K ESC [G —
// erase the line, back to column 0, the effect of the CR the scrub removes —
// or any other terminal control or line separator reach the rendered line as
// spaces, one per control, so a record cannot forge or hide another on the
// console. A string VALUE is quoted by strconv.AppendQuote, which escapes all
// of them, and is held to the same check.
func TestTextEncoderNeutralisesTerminalControls(t *testing.T) {
	t.Parallel()
	forged := "login failed for bob\x1b[2K\x1b[G2026-10-21T00:00:00.000Z INFO login ok user=admin"
	enc := encoder.NewText(nil)
	for _, c := range append([]string{forged}, terminalControls...) {
		rec := corelogger.RecordEvent{
			Time:    time.Unix(0, 0),
			Level:   level.Info,
			Message: "m" + c + "m",
			Attrs:   []corelogger.AttrValue{{Key: "k" + c + "k", Value: corelogger.StringValue("v" + c + "v")}},
		}
		line := strings.TrimSuffix(string(enc.Append(nil, []string{"g" + c + "g"}, rec)), "\n")
		for _, bad := range append([]string{"\n", "\r", "\x00"}, terminalControls...) {
			if strings.Contains(line, bad) {
				t.Fatalf("the rendered line carries %q verbatim: %q", bad, line)
			}
		}
		if c != forged && (!strings.Contains(line, " m m ") || !strings.Contains(line, " g g.k k=")) {
			t.Fatalf("the control %q did not become one space: %q", c, line)
		}
	}
}

// TestAppendSanitizedKeepsWhatIsNotAControl pins the other side: printable
// text in any script, emoji, the runes beside the scrubbed ones, and bytes
// that are not valid UTF-8 are copied unchanged, so a log line keeps saying
// what it was given.
func TestAppendSanitizedKeepsWhatIsNotAControl(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"plain ASCII ~!@#$%^&*()_+{}|:<>?",
		"café ñ ü — “quoted” …",
		"名前 이름 имя اسم",
		"😀 \U0010FFFF",
		" ¿‧‪‰",
		"\xff\xfe\x80\x85\xc2",
	} {
		if got := string(encoder.AppendSanitized(nil, in)); got != in {
			t.Errorf("AppendSanitized(%q) = %q, want it unchanged", in, got)
		}
	}
}
