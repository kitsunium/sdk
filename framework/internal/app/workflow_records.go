package app

import (
	"encoding/json"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// A workflow's journal on a database (workflow_ports.go's sqlJournal): its
// records — the Studio's instances — kept by the store engine over
// documents (ADR 0179 §2), one per entity, in the table
// <service>__<workflow>__workflow.

// instanceConfig is the engine of a workflow's journal on a database: its
// records, keyed by their entity's key.
var instanceConfig = &engineConfig{
	key: func(d plug.DocumentValue) string {
		inst, _ := d.Value.(model.Instance)
		return inst.ID
	},
	decode: func(raw json.RawMessage) (any, error) {
		var inst model.Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return nil, undecodable("workflow", err)
		}
		return inst, nil
	},
}

// instancesOf are the journal's records ds hold.
func instancesOf(ds []plug.DocumentValue, err error) ([]model.Instance, error) {
	if err != nil {
		return nil, err
	}
	out := make([]model.Instance, 0, len(ds))
	for _, d := range ds {
		inst, ok := d.Value.(model.Instance)
		if !ok {
			v, err := instanceConfig.decode(d.JSON)
			if err != nil {
				return nil, err
			}
			inst, _ = v.(model.Instance)
		}
		out = append(out, inst)
	}
	return out, nil
}

// instanceDoc is inst as the journal's engine keeps it.
func instanceDoc(inst model.Instance) (plug.DocumentValue, error) {
	raw, err := json.Marshal(inst)
	if err != nil {
		return plug.DocumentValue{}, unencodable("workflow", err)
	}
	return plug.DocumentValue{JSON: raw, Value: inst}, nil
}
