package async

import "testing"

func Test_newRecordEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"factory returns a non-nil entry with nil data slice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRecordEntry()
			//: factory contract: never nil; the rest depends on this invariant.
			if e == nil {
				t.Fatal("newRecordEntry returned nil")
				return
			}
			if e.data != nil {
				t.Errorf("fresh entry data = %v, want nil", e.data)
			}
		})
	}
}
