package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"iter"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A workflow's journal as a data copy moves it (ADR 0178 C7): the file
// <service>/<workflow>.workflow.json under the data directory, read as
// documents by key, written in the journal's table on the database.

// jsonDocs is a file of documents by key — a workflow's journal —, as the
// copy reads it.
type jsonDocs struct {
	docs map[string]json.RawMessage
	keys []string
}

// journalPart is the workflow's journal: read from its file under the data
// directory, written in its table on r. A workflow that never ran has no
// file, and nothing to copy.
func (w *WorkflowService[E, S]) journalPart(a *App, r *databaseRun) (*copyPart, error) {
	from, err := readJSONDocs(a, w.journalFile())
	if err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's file cannot be read", err, errs.String("workflow", w.id))
	}
	j, err := w.openSQLJournal(a, r)
	if err != nil {
		return nil, err
	}
	to, ok := documentsOf(j.eng)
	sink, sinks := to.(copySink)
	if !ok || !sinks {
		return nil, failure(CodeDataCopy, "DATA_COPY_STORE", "a workflow's table cannot be written by a copy", nil, errs.String("workflow", w.id))
	}
	return &copyPart{name: w.journalTable(), from: from, to: sink}, nil
}

// journalFile is the workflow's journal under the data directory.
func (w *WorkflowService[E, S]) journalFile() string {
	return w.svc.name + "/" + w.name + ".workflow.json"
}

// readJSONDocs reads the file name under the data directory; one that does
// not exist holds nothing.
func readJSONDocs(a *App, name string) (*jsonDocs, error) {
	out := &jsonDocs{docs: map[string]json.RawMessage{}}
	raw, err := fs.ReadFile(a.data, name)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out.docs); err != nil {
		return nil, err
	}
	out.keys = slices.Sorted(maps.Keys(out.docs))
	return out, nil
}

// KeyList yields the file's keys, sorted.
func (j *jsonDocs) KeyList(_ context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for _, k := range j.keys {
			if !yield(k, nil) {
				return
			}
		}
	}
}

// TombstoneList is none: a journal keeps no trash.
func (*jsonDocs) TombstoneList(_ context.Context) ([]plug.TombstoneValue, error) { return nil, nil }

// VersionList is none: a journal keeps no versions.
func (*jsonDocs) VersionList(_ context.Context, _ string) ([]plug.VersionValue, error) {
	return nil, nil
}

// Current is the document key as the file holds it.
func (j *jsonDocs) Current(_ context.Context, key string) (json.RawMessage, error) {
	raw, ok := j.docs[key]
	if !ok {
		return nil, frameworkRefusal(plug.RefusalNotFound, "workflow journal")
	}
	return raw, nil
}

// KeepsVersions reports no: a journal keeps none.
func (*jsonDocs) KeepsVersions() bool { return false }
