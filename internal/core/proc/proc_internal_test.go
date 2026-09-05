package proc

import "testing"

// The name table is what String reads, and a resource added to the constants
// without an entry would report as "unknown" — indistinguishable from the
// reserved zero value. Checking the table against Known catches that at the
// point the constant is introduced rather than in a log months later.
func Test_resourceNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		resource Resource
		want     string
	}
	tests := []tc{
		{"nofile", ResourceNoFile, "nofile"},
		{"nproc", ResourceNProc, "nproc"},
		{"core", ResourceCore, "core"},
		{"as", ResourceAS, "as"},
		{"cpu", ResourceCPU, "cpu"},
		{"fsize", ResourceFSize, "fsize"},
		{"data", ResourceData, "data"},
		{"stack", ResourceStack, "stack"},
		{"memlock", ResourceMemLock, "memlock"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := resourceNames[c.resource]
		if !ok {
			t.Fatalf("resource %d has no name in the table", c.resource)
		}
		if got != c.want {
			t.Errorf("name = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The table and the Known range must agree in both directions: a named
// resource outside the range would be unreachable, and a resource inside it
// without a name would render as "unknown".
func Test_resourceNamesMatchesTheKnownRange(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func(t *testing.T)
	}
	tests := []tc{
		{"every Known resource is named", func(t *testing.T) {
			t.Helper()
			for r := ResourceUnknown + 1; r.Known(); r++ {
				if _, ok := resourceNames[r]; !ok {
					t.Errorf("resource %d is Known but unnamed", r)
				}
			}
		}},
		{"every named resource is Known", func(t *testing.T) {
			t.Helper()
			for r := range resourceNames {
				if !r.Known() {
					t.Errorf("resource %d is named %q but not Known", r, resourceNames[r])
				}
			}
		}},
		{"the counts agree", func(t *testing.T) {
			t.Helper()
			if len(resourceNames) != int(ResourceMemLock) {
				t.Errorf("table holds %d names for %d resources", len(resourceNames), int(ResourceMemLock))
			}
		}},
		{"the zero value is never named", func(t *testing.T) {
			t.Helper()
			if _, ok := resourceNames[ResourceUnknown]; ok {
				t.Error("the reserved zero value has a name; it must report as unknown")
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.check(t)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
