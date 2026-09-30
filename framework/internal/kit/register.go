// Package kit — the register of processing: each store's line.
package kit

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// The register of processing (ADR 0006 §8): the record GDPR art. 30(1) asks
// a controller to keep, as far as the code knows it. Per store that keeps
// personal data:
//
//   - point (b), the purpose: kit.Purpose;
//   - point (c), the categories of subjects and of personal data: its
//     subject field, and its fields by class;
//   - point (d), the recipients inside the product: every node the graph
//     shows reading the store, and every mailer that carries its data out;
//   - point (f), the time limits for erasure: its retention — or the
//     product's own, in its words (RetentionByProduct);
//   - point (g), the security measures kit takes.
//
// The code does not know the controller and its representatives (point
// a) or the transfers (point e): the register marks them to complete. A
// store with personal data but no purpose, no retention or no subject is
// listed with its gaps — a store whose product keeps its retention lacks
// neither a retention nor a subject. kit's own stores — the holds and the journal — are
// not the product's processing and are left out.

// registerLine is the store's line in the register.
func (s *StoreService[T]) registerLine(a *App) model.RegisterStore {
	plan := s.plan()
	line := model.RegisterStore{Store: s.id, Subject: plan.subjectPointer(), Security: securityOf(plan, s.sealsAtRest(a))}
	line.Personal, line.Special, line.Secret = fieldsOf(plan)
	if p := s.privacy; p != nil {
		p.registerDeclared(a, &line)
	}
	if plan.personal() {
		line.Gaps = gapsOf(line)
	}
	return line
}

// securityOf are the measures kit takes for a store's personal data: sealed
// says the store seals it at rest.
func securityOf(plan *classPlan, sealed bool) []string {
	out := []string{model.MeasureRedacted, model.MeasureHolds, model.MeasureJournal}
	if plan.sensitive() {
		out = append(out, measureOf(sealed))
	}
	return out
}

// registerDeclared fills a register line with what the store declares: its
// purpose, and its time limits.
func (p *storePrivacy[T]) registerDeclared(a *App, line *model.RegisterStore) {
	line.Purpose = p.purpose
	line.Erase, _ = retentionText(a, p.erase)
	line.Delete, _ = retentionText(a, p.delete)
	if p.byProduct != nil {
		line.ByProduct = new(*p.byProduct)
	}
	if p.heldUntil == nil {
		return
	}
	fn := "an instant the record carries"
	if p.heldAt != nil && p.heldAt.fn != "" {
		fn = shortFunc(p.heldAt.fn)
	}
	line.HeldUntil = "until " + fn
	if p.heldReason != "" {
		line.HeldUntil += " (" + p.heldReason + ")"
	}
}

// gapsOf are what a register line lacks: a purpose, a retention, a subject
// — the last two none when the product keeps the store's retention itself.
func gapsOf(line model.RegisterStore) []string {
	var gaps []string
	if line.Purpose == "" {
		gaps = append(gaps, model.GapPurpose)
	}
	if line.ByProduct != nil {
		return gaps
	}
	if line.Erase == "" && line.Delete == "" {
		gaps = append(gaps, model.GapRetention)
	}
	if line.Subject == "" {
		gaps = append(gaps, model.GapSubject)
	}
	return gaps
}

// register is the app's record of processing, the recipients read from the
// graph g: what it draws reading each store, declared, found in the code or
// seen at run time.
func (a *App) register(g *model.Graph) model.Register {
	reg := model.Register{App: a.name, ToComplete: []string{"a", "e"}}
	recipients := recipientsOf(g)
	for _, st := range a.productStores() {
		if !st.plan().personal() && !st.privacyDeclared() {
			continue
		}
		line := st.registerLine(a)
		line.Recipients = recipients[line.Store]
		if g != nil && line.Subject != "" {
			line.SubjectDoc, _ = subjectDoc(g, line.Store, line.Subject)
		}
		reg.Stores = append(reg.Stores, line)
	}
	return reg
}

// recipients are, per store, who inside the product reads it, as the app's
// graph knows it now.
func (a *App) recipients() map[string][]string {
	return recipientsOf(a.Graph())
}

// recipientsOf reads the recipients from a graph: the nodes a reads edge
// leaves for each store, and the mailers those nodes send through — a
// connector that carries the data out.
func recipientsOf(g *model.Graph) map[string][]string {
	out := map[string][]string{}
	if g == nil {
		return out
	}
	mailers := mailersOf(g)
	for _, e := range g.Edges {
		if productReads(e) {
			addRecipients(out, e.To, append([]string{e.From}, mailers[e.From]...))
		}
	}
	return out
}

// mailersOf are, per node, the mailers it sends through.
func mailersOf(g *model.Graph) map[string][]string {
	out := map[string][]string{}
	for _, e := range g.Edges {
		if e.Kind == model.EdgeSends {
			out[e.From] = append(out[e.From], e.To)
		}
	}
	return out
}

// productReads reports whether e is a node of the product reading a store:
// not the outside world, not kit's own service.
func productReads(e model.Edge) bool {
	return e.Kind == model.EdgeReads && e.From != "" && e.From != model.ExternalID && model.ServiceOf(e.From) != privacyService
}

// addRecipients adds ids to the recipients of store, each once, in order.
func addRecipients(out map[string][]string, store string, ids []string) {
	for _, id := range ids {
		if !slices.Contains(out[store], id) {
			out[store] = append(out[store], id)
		}
	}
	slices.Sort(out[store])
}

// subjectDoc is the comment of a store's subject field, when the analyzer
// read it.
func subjectDoc(g *model.Graph, store, pointer string) (string, bool) {
	n := g.Node(store)
	if n == nil || n.Store == nil || n.Store.Entity == nil {
		return "", false
	}
	s := n.Store.Entity
	for seg := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		var next *model.Schema
		for _, f := range s.Fields {
			if escapePointer(f.Name) == seg {
				if f.Subject {
					return f.Doc, true
				}
				next = f.Type
			}
		}
		if next == nil {
			return "", false
		}
		s = next
	}
	return "", false
}

var (
	// measureWords says a measure in English, for the command line.
	measureWords = map[string]string{
		model.MeasureRedacted:  "never shown in the Studio, the spans or the logs",
		model.MeasureHolds:     "legal holds stop erasure and deletion",
		model.MeasureJournal:   "every export, erasure and hold, and every deletion kit makes — retention, a person's erasure —, journaled and hash-chained",
		model.MeasureNotSealed: "in clear: the store is kept in memory, where nothing is at rest, or its fields are tagged plain",
		model.MeasureSealed:    "sealed at rest, AES-256-GCM, under one data key per person that their erasure destroys",
	}

	// gapWords say a gap in English, for the command line.
	gapWords = map[string]string{
		model.GapPurpose:   "no purpose: declare kit.Purpose",
		model.GapRetention: "no retention: its personal data is kept until the product deletes it",
		model.GapSubject:   "no subject: no person can have these records exported or erased",
	}
)

// writeRegister prints the register for a person to read and complete.
func writeRegister(w io.Writer, reg model.Register) {
	fmt.Fprintf(w, "# Record of processing — %s (GDPR art. 30(1)), as far as the code knows it\n\n", reg.App)
	fmt.Fprintln(w, "To complete by the controller:")
	fmt.Fprintln(w, "  (a) the controller, its representative and its data protection officer")
	fmt.Fprintln(w, "  (e) the transfers to a third country or an international organisation")
	if len(reg.Stores) == 0 {
		fmt.Fprintln(w, "\nNo store keeps personal data: no field is classified personal or special.")
		return
	}
	for _, s := range reg.Stores {
		writeRegisterStore(w, s)
	}
}

// writeRegisterStore prints one store's line of the register.
func writeRegisterStore(w io.Writer, s model.RegisterStore) {
	fmt.Fprintf(w, "\n## %s\n", s.Store)
	for _, l := range [][2]string{
		{"purpose (b)", s.Purpose},
		{"subjects (c)", subjectWords(s)},
		{"personal (c)", strings.Join(s.Personal, ", ")},
		{"special (c)", specialText(s.Special)},
		{"credentials", strings.Join(s.Secret, ", ")},
		{"recipients (d)", strings.Join(s.Recipients, ", ")},
		{"erased (f)", s.Erase},
		{"deleted (f)", s.Delete},
		{"retention (f)", byProductWords(s.ByProduct)},
		{"held (f)", s.HeldUntil},
	} {
		if l[1] != "" {
			fmt.Fprintf(w, "  %-16s %s\n", l[0]+":", l[1])
		}
	}
	for i, m := range s.Security {
		label := ""
		if i == 0 {
			label = "security (g):"
		}
		fmt.Fprintf(w, "  %-16s %s\n", label, measureWords[m])
	}
	for _, gap := range s.Gaps {
		fmt.Fprintf(w, "  %-16s %s\n", "GAP:", gapWords[gap])
	}
}

// byProductWords says a retention the product keeps itself: "by the
// product", and its own words when it gave some; nothing for a store whose
// retention is kit's.
func byProductWords(r *model.ProductRetention) string {
	var words []string
	if r != nil {
		words = append(words, "kept by the product itself")
		if r.Limits != "" {
			words = append(words, r.Limits)
		}
	}
	return strings.Join(words, ": ")
}

// subjectWords names a store's subjects: its subject field, and its doc.
func subjectWords(s model.RegisterStore) string {
	if s.SubjectDoc == "" {
		return s.Subject
	}
	return s.Subject + " — " + s.SubjectDoc
}

// specialText lists the special categories of data, naming the article of
// the GDPR that governs them when there are any.
func specialText(special []string) string {
	text := strings.Join(special, ", ")
	if len(special) > 0 {
		text += " (GDPR art. 9)"
	}
	return text
}
