package kit

import (
	"testing"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// The model's table names fit what the SDK's document store takes.
func TestTableNamesFitTheDocumentStore(t *testing.T) {
	if model.MaxTableLen != docstore.MaxSQLTableLen {
		t.Errorf("the model names tables up to %d bytes, the SDK takes %d", model.MaxTableLen, docstore.MaxSQLTableLen)
	}
}
