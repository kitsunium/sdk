package csv_test

import "strconv"

// appendFixtures are the values whose Append must write Marshal's bytes:
// every quoting rule encoding/csv applies, and the promotion shape.
func appendFixtures() map[string]any {
	return map[string]any{
		"plain":           [][]string{{"name", "age"}, {"alice", "30"}},
		"quoted":          [][]string{{"a,b", `say "hi"`, "two\nlines", "cr\rhere", " lead", `\.`, ""}},
		"empty record":    [][]string{{}, {"x"}},
		"no records":      [][]string{},
		"pointer":         &[][]string{{"p"}},
		"promotion shape": [][]string{{"_json"}, {`{"a":"b"}`}},
		"one kibibyte":    kibValue(),
	}
}

// appendFailure is a value Marshal refuses.
func appendFailure() any {
	//: not a records matrix.
	return []int{1, 2, 3}
}

// kibValue is a table of about 1 KiB.
func kibValue() [][]string {
	rows := make([][]string, 0, 40)
	for i := range 40 {
		rows = append(rows, []string{"row-" + strconv.Itoa(i), "value", strconv.Itoa(i * 7919), "x,y"})
	}
	return rows
}
