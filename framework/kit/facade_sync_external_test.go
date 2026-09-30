package kit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// facadeNames are the names this facade gives the internal declarations it
// renames: the internal name carries its role, the facade keeps the name a
// product writes.
var facadeNames = [][2]string{
	{"AuthenticatorHandler", "Authenticator"},
	{"StdioValue", "Stdio"},
	{"DatabaseURLValue", "DatabaseURL"},
	{"EmptyValue", "Empty"},
	{"EndpointService", "Endpoint"},
	{"ViolationMessage", "Violation"},
	{"WakeEvent", "Wake"},
	{"RevisionEvent", "Revision"},
	{"PasswordPolicyService", "PasswordPolicy"},
	{"PortService", "Port"},
	{"FieldRefValue", "FieldRef"},
	{"RecordsService", "Records"},
	{"SettingService", "Setting"},
	{"StoreService", "Store"},
	{"TopicService", "Topic"},
	{"SubscriptionWorker", "Subscription"},
	{"WrittenEvent", "Written"},
	{"WorkflowService", "Workflow"},
	{"ChangeEvent", "Change"},
	{"AppConfigurer", "AppOption"},
	{"CommandConfigurer", "CommandOption"},
	{"DatabaseConfigurer", "DatabaseOption"},
	{"EndpointConfigurer", "EndpointOption"},
	{"ExposeConfigurer", "ExposeOption"},
	{"ListenerConfigurer", "ListenerOption"},
	{"LoopConfigurer", "LoopOption"},
	{"MailerConfigurer", "MailerOption"},
	{"ModuleConfigurer", "ModulePart"},
	{"MountConfigurer", "MountOption"},
	{"PasswordConfigurer", "PasswordOption"},
	{"PortConfigurer", "PortOption"},
	{"QueryConfigurer", "QueryOption"},
	{"SecretConfigurer", "SecretOption"},
	{"SettingConfigurer", "SettingOption"},
	{"StaticConfigurer", "StaticOption"},
	{"StoreConfigurer", "StoreOption"},
	{"SubscriptionConfigurer", "SubscriptionOption"},
	{"Keeper", "Keepable"},
	{"AttrsProvider", "Principal"},
	{"CommandLineConfigurer", "CLIOption"},
	{"ScopeValue", "Scope"},
	{"ActivityHandler", "Activity"},
}

// internalOnly are the exported internal names a product never reaches: the
// constructors a declaring method calls, and the hooks a subsystem package
// calls as it is imported (framework/kit/server, studio, config/*), which the
// facade does not export.
var internalOnly = []string{
	"NewCommand", "NewEndpointService", "NewError", "NewListener", "NewLoop",
	"NewMailer", "NewPortService", "NewQuery", "NewRecordsService", "NewSecret",
	"NewSettingService", "NewStoreService", "NewTopicService", "NewWorkflowService",
	"RegisterConfigFormat", "EnableStudio", "EnableServer",
}

// topLevelNames are the exported top-level names the Go files of dir declare,
// tests left out; methods are not top-level.
func topLevelNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for name, obj := range f.Scope.Objects {
			if ast.IsExported(name) && obj.Kind != ast.Bad {
				names[name] = true
			}
		}
	}
	return names
}

// Every exported name of framework/internal/kit is one of the facade's —
// under its product-facing name when the facade renames it —, and the facade
// declares nothing the implementation does not: a name added to one and
// forgotten in the other fails here.
func TestTheFacadeMirrorsTheImplementation(t *testing.T) {
	internal := topLevelNames(t, filepath.Join("..", "internal", "kit"))
	facade := topLevelNames(t, ".")
	public := make(map[string]string, len(facadeNames))
	for _, pair := range facadeNames {
		public[pair[0]] = pair[1]
	}
	want := map[string]bool{}
	for name := range internal {
		if slices.Contains(internalOnly, name) {
			continue
		}
		if pub, renamed := public[name]; renamed {
			name = pub
		}
		want[name] = true
		if !facade[name] {
			t.Errorf("framework/kit lacks %s", name)
		}
	}
	for name := range facade {
		if !want[name] {
			t.Errorf("framework/kit declares %s, which framework/internal/kit does not", name)
		}
	}
}
