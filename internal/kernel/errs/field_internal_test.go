package errs

import "testing"

func Test_fieldKindConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  fieldKind
		want fieldKind
	}{
		{"invalid is zero", fieldInvalid, 0},
		{"string is 1", fieldString, 1},
		{"int is 2", fieldInt, 2},
		{"bool is 3", fieldBool, 3},
		{"float is 4", fieldFloat, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

func Test_FieldValueZeroStringValueIsEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"zero value renders empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var zero FieldValue
			if got := zero.StringValue(); got != "" {
				t.Errorf("zero FieldValue StringValue = %q, want empty", got)
			}
		})
	}
}
