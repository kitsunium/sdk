package app

import (
	"reflect"

	"github.com/kitsunium/sdk/framework/model"
)

// The declarations that use a type -------------------------------------------

// typeSource is a building block whose values kit shows or keeps: a store's
// entity, an endpoint's request and response, a topic's message, an auth
// handler's credentials and data, a command's and a query's input and result
// — a queued command's input kept in its queue.
type typeSource interface {
	dataTypes() []dataType
}

// dataType is a Go type a declaration carries, and whether kit reads one
// person's identity from its values: a store's entity, a topic's message
// and a queued command's input rest and are sealed under their subject's
// key (ADR 0178, C5); every other type only passes through, and its
// subject is personal data for redaction and nothing else.
type dataType struct {
	typ   reflect.Type
	rests bool
}

// passing are types kit reads no identity from.
func passing(types ...reflect.Type) []dataType {
	out := make([]dataType, 0, len(types))
	for _, t := range types {
		out = append(out, dataType{typ: t})
	}
	return out
}

// dataTypes are the Go types the Store carries, for the classification of
// personal data: its entity rests.
func (s *storeCore) dataTypes() []dataType {
	return []dataType{{typ: s.ent.typ, rests: true}}
}

// dataTypes are the Go types the Topic carries, for the classification of
// personal data: its message rests in its subscriptions' queues.
func (t *TopicService[T]) dataTypes() []dataType {
	return []dataType{{typ: reflect.TypeFor[T](), rests: true}}
}

// dataTypes are the Go types the Endpoint carries, for the classification of
// personal data.
func (e *EndpointService[Req, Resp]) dataTypes() []dataType {
	return passing(reflect.TypeFor[Req](), reflect.TypeFor[Resp]())
}

// dataTypes are the Go types the Authenticator carries, for the classification
// of personal data.
func (h *AuthenticatorHandler[P, D]) dataTypes() []dataType {
	return passing(reflect.TypeFor[P](), reflect.TypeFor[D]())
}

// dataTypes are the Go types the Command carries, for the classification of
// personal data: a queued command's input rests in its queue.
func (c *Command[C, R]) dataTypes() []dataType {
	return []dataType{{typ: reflect.TypeFor[C](), rests: c.queued()}, {typ: reflect.TypeFor[R]()}}
}

// dataTypes are the Go types the Query carries, for the classification of
// personal data.
func (q *Query[Q, R]) dataTypes() []dataType {
	return passing(reflect.TypeFor[Q](), reflect.TypeFor[R]())
}

// classificationProblems judges the app tags of every type the app's
// declarations show or keep, each type once, at the first declaration that
// uses it: what a tag gets wrong is an error the start refuses. What is
// wrong only where kit reads an identity — a subject in a list — is said
// once more per type, at the first declaration where the type rests. With
// explain, a field whose name and type read like personal data and which
// has no class — or the class secret, a credential's — is a warning, said
// once per declared field — at the first declaration that reaches it,
// however many types it is promoted into.
func (a *App) classificationProblems(explain bool) []model.Diagnostic {
	j := typeJudge{a: a, seenType: map[reflect.Type]bool{}, seenResting: map[reflect.Type]bool{}, seenWarning: map[declaredField]bool{}}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if tn, ok := n.(typeSource); ok {
				j.judge(n.base(), tn.dataTypes(), explain && !kitOwn(svc))
			}
		}
	}
	return j.out
}

// typeJudge says what the types of the app's declarations get wrong: each
// type once, what is wrong with it at rest once, each declared field's
// warning once.
type typeJudge struct {
	a           *App
	seenType    map[reflect.Type]bool
	seenResting map[reflect.Type]bool
	seenWarning map[declaredField]bool
	out         []model.Diagnostic
}

// judge says what the types a declaration uses get wrong, at the
// declaration: their errors — those of a type at rest only where it
// rests —, and with warn their warnings.
func (j *typeJudge) judge(b *nodeBase, types []dataType, warn bool) {
	for _, d := range types {
		if d.typ == nil {
			continue
		}
		p := planOf(d.typ)
		if d.rests && !j.seenResting[d.typ] {
			j.seenResting[d.typ] = true
			j.say(b, p.resting)
		}
		if j.seenType[d.typ] {
			continue
		}
		j.seenType[d.typ] = true
		j.say(b, p.problems)
		if warn {
			j.warn(b, p.warnings)
		}
	}
}

// say adds each problem as an error at the declaration.
func (j *typeJudge) say(b *nodeBase, problems []phrase) {
	for _, problem := range problems {
		j.out = append(j.out, diagnosticOf("error", b.id, j.a.source(&b.decl), problem))
	}
}

// warn says the warning of each field not warned of yet, at the
// declaration.
func (j *typeJudge) warn(b *nodeBase, warnings []fieldWarning) {
	for _, w := range warnings {
		if !j.seenWarning[w.field] {
			j.seenWarning[w.field] = true
			j.out = append(j.out, diagnosticOf("warning", b.id, j.a.source(&b.decl), w.said))
		}
	}
}
