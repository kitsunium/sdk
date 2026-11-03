package csv_test

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/csv"
)

// benchOrder is a row of a typical export: an id, a label, a price, a flag.
type benchOrder struct {
	ID    int64   `csv:"id"`
	Item  string  `csv:"item"`
	Price float64 `csv:"price"`
	Paid  bool    `csv:"paid"`
}

// benchOrders is 1 000 rows.
func benchOrders() []benchOrder {
	out := make([]benchOrder, 1000)
	for i := range out {
		out[i] = benchOrder{ID: int64(i), Item: "item-" + strconv.Itoa(i), Price: float64(i) / 4, Paid: i%2 == 0}
	}
	return out
}

// benchTable is those rows as CSV.
func benchTable(b *testing.B) []byte {
	b.Helper()
	data, err := csv.New().Marshal(benchOrders())
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkUnmarshalTyped1000 reads 1 000 rows straight into []benchOrder
// (#902).
func BenchmarkUnmarshalTyped1000(b *testing.B) {
	data := benchTable(b)
	c := csv.New()
	b.ReportAllocs()
	for b.Loop() {
		var out []benchOrder
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUnmarshalRecordsAndConvert1000 is what a caller did before #902:
// Unmarshal into [][]string, then convert each cell by hand.
func BenchmarkUnmarshalRecordsAndConvert1000(b *testing.B) {
	data := benchTable(b)
	c := csv.New()
	b.ReportAllocs()
	for b.Loop() {
		var records [][]string
		if err := c.Unmarshal(data, &records); err != nil {
			b.Fatal(err)
		}
		out := make([]benchOrder, 0, len(records)-1)
		for _, rec := range records[1:] {
			id, _ := strconv.ParseInt(rec[0], 10, 64)
			price, _ := strconv.ParseFloat(rec[2], 64)
			paid, _ := strconv.ParseBool(rec[3])
			out = append(out, benchOrder{ID: id, Item: rec[1], Price: price, Paid: paid})
		}
		_ = out
	}
}

// BenchmarkMarshalTyped1000 writes the 1 000 rows with their header.
func BenchmarkMarshalTyped1000(b *testing.B) {
	rows := benchOrders()
	var v any = rows
	c := csv.New()
	b.ReportAllocs()
	for b.Loop() {
		out, err := c.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}
