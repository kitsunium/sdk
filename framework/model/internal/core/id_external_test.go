package core_test

import (
	"regexp"
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

func TestParseIDRoundTrips(t *testing.T) {
	cases := []struct {
		in   string
		want model.IDValue
	}{
		{"external", model.IDValue{Kind: model.KindExternal, Name: "external"}},
		{"todos", model.IDValue{Service: "todos", Kind: model.KindService, Name: "todos"}},
		{"moderation.intake", model.IDValue{Module: "moderation", Service: "intake", Kind: model.KindService, Name: "moderation.intake"}},
		{"todos/store/todos", model.IDValue{Service: "todos", Kind: model.KindStore, Name: "todos"}},
		{"todos/endpoint/GET /todos/{id}", model.IDValue{Service: "todos", Kind: model.KindEndpoint, Name: "GET /todos/{id}"}},
		{"moderation.intake/command/Flag", model.IDValue{Module: "moderation", Service: "intake", Kind: model.KindCommand, Name: "Flag"}},
		{"render/listener/socket", model.IDValue{Service: "render", Kind: model.KindListener, Name: "socket"}},
		{"render/cli/status", model.IDValue{Service: "render", Kind: model.KindCLI, Name: "status"}},
		{"binary:statusline", model.IDValue{Scope: "statusline", Kind: model.KindBinary, Name: "statusline"}},
		{"binary:statusline/role/daemon", model.IDValue{Scope: "statusline", Kind: model.KindRole, Name: "daemon"}},
		{"library:textwidth", model.IDValue{Scope: "textwidth", Kind: model.KindLibrary, Name: "textwidth"}},
	}
	pattern := regexp.MustCompile(model.IDPattern)
	for _, c := range cases {
		got, err := model.ParseID(c.in)
		if err != nil {
			t.Errorf("ParseID(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseID(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if got.String() != c.in {
			t.Errorf("ParseID(%q).String() = %q", c.in, got.String())
		}
		if !pattern.MatchString(c.in) {
			t.Errorf("IDPattern refuses %q, which ParseID accepts", c.in)
		}
	}
}

func TestParseIDRefusesWithoutQuotingTheInput(t *testing.T) {
	bad := []string{
		"", "Todos", "todos/", "todos/store", "todos/nosuchkind/x", "todos/store/9lives",
		"todos/store/GET /todos", "a.b.c", ".todos", "binary:", "binary:x/role/", "binary:X",
		"library:", "todos/role/daemon", "todos/binary/x",
		"s3ntinel-model-5ec7e7/store/x y",
	}
	pattern := regexp.MustCompile(model.IDPattern)
	for _, in := range bad {
		_, err := model.ParseID(in)
		if !errs.HasCode(err, model.CodeInvalidID) {
			t.Errorf("ParseID(%q) = %v, want INVALID_ID", in, err)
			continue
		}
		if strings.Contains(err.Error(), "s3ntinel") {
			t.Errorf("the refusal quotes the input: %v", err)
		}
		if pattern.MatchString(in) {
			t.Errorf("IDPattern accepts %q, which ParseID refuses", in)
		}
	}
}

func TestNodeIDAgreesWithTheGrammar(t *testing.T) {
	id := model.NodeID(model.QualifiedService("moderation", "intake"), model.KindQuery, "Pending")
	if id != "moderation.intake/query/Pending" {
		t.Fatalf("NodeID = %q", id)
	}
	if _, err := model.ParseID(id); err != nil {
		t.Fatalf("NodeID built %q, which ParseID refuses: %v", id, err)
	}
	if got := model.RoleID("statusline", "daemon"); got != "binary:statusline/role/daemon" {
		t.Errorf("RoleID = %q", got)
	}
	if !model.ValidContract("render/v1") || model.ValidContract("render") || model.ValidContract("render/v0") {
		t.Error("ValidContract disagrees with the contract grammar")
	}
}
