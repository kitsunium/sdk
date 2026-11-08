package service

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// firstPassedFD is the lowest descriptor a hand-over names: 0, 1 and 2 are
// the stdio every image keeps.
const firstPassedFD int = 3

// The supervisor's hand-over (S0h, kitsunium/sdk#738; ADR 0214 §11 and its
// amendment): Freeze ends units without stopping a process, Reexec executes
// the image again handing their state over, TakeHandover reads it in the
// incoming image, Resume makes each unit from it. Linux only.

// HandoverState is what an outgoing image hands the incoming one (S0h):
// each frozen unit's state, versioned, and the descriptors it keeps.
// Freeze makes one, Reexec passes it, TakeHandover reads it, Resume takes
// each unit from it. It is not safe for concurrent use.
type HandoverState struct {
	units []*frozenUnit
}

// frozenUnit is one unit as Freeze left it: its model written down, its
// processes — the outgoing image's handles, or none in the incoming one —
// and the descriptors it keeps, each a descriptor of its own.
type frozenUnit struct {
	// store holds the store's descriptors by entry id.
	store map[uint64]*os.File
	// conn is an instance's connection (S0f), notify the notification
	// socket ListenNotify made, bound at socket.
	conn, notify *os.File
	name, socket string
	model        []byte
	procs        []handedProc
	taken        bool
}

// handedProc is one process a frozen unit runs.
type handedProc struct {
	// proc is the outgoing image's handle; nil once the image changed.
	proc coreproc.Process
	// witness is a handle on a process the unit adopted — not its child —,
	// which proves the pid still names it in the incoming image.
	witness *os.File
	pid     int
	// child says the engine spawned it: the image's child still.
	child bool
}

// restoring is what a resumed unit's actor starts from; ready is sent once it
// did, so Resume returns a unit whose state is the one handed over.
type restoring struct {
	fu    *frozenUnit
	ready chan struct{}
	m     model
}

// NewHandoverState is a hand-over holding no unit yet, what Freeze fills.
func NewHandoverState() *HandoverState {
	//: empty.
	return &HandoverState{}
}

// Units names the units h holds that no Resume took yet.
func (h *HandoverState) Units() []string {
	var out []string
	//: each unit still held.
	for _, fu := range h.units {
		//: not taken.
		if !fu.taken {
			out = append(out, fu.name)
		}
	}
	//: the names, in the order they were frozen.
	return out
}

// Close ends what no Resume took: each such unit's processes stopped in
// order — SIGTERM, grace, SIGKILL — and reaped where they are children,
// never left unsupervised, and every descriptor h still holds closed.
// When ctx ends first the kills are sent at once.
func (h *HandoverState) Close(ctx context.Context, grace time.Duration) error {
	//: each unit no Resume took.
	for _, fu := range h.units {
		//: taken: the unit owns what it held.
		if fu.taken {
			continue
		}
		fu.taken = true
		stopHanded(ctx, grace, fu.procs)
		fu.closeFiles()
	}
	//: ended.
	return nil
}

// unit is the state h holds for name, nil when none.
func (h *HandoverState) unit(name string) *frozenUnit {
	//: each unit.
	for _, fu := range h.units {
		//: this one, not taken yet.
		if fu.name == name && !fu.taken {
			//: found.
			return fu
		}
	}
	//: none.
	return nil
}

// files lists every descriptor fu holds: the witnesses, the store's by id,
// the connection, the socket.
func (fu *frozenUnit) files() []*os.File {
	var out []*os.File
	//: each process's witness.
	for _, p := range fu.procs {
		//: an adopted one's.
		if p.witness != nil {
			out = append(out, p.witness)
		}
	}
	//: the store, by id.
	for _, id := range slices.Sorted(mapKeys(fu.store)) {
		out = append(out, fu.store[id])
	}
	//: the connection and the socket.
	for _, f := range []*os.File{fu.conn, fu.notify} {
		//: held.
		if f != nil {
			out = append(out, f)
		}
	}
	//: every one.
	return out
}

// closeFiles closes every descriptor fu holds.
func (fu *frozenUnit) closeFiles() {
	closeFiles(fu.files())
	clear(fu.store)
	fu.conn, fu.notify = nil, nil
	//: each witness.
	for i := range fu.procs {
		fu.procs[i].witness = nil
	}
}

// mapKeys lists a store's ids.
func mapKeys(m map[uint64]*os.File) func(func(uint64) bool) {
	//: the iterator.
	return func(yield func(uint64) bool) {
		//: each id.
		for id := range m {
			//: until the caller stops.
			if !yield(id) {
				return
			}
		}
	}
}

// Freeze hands units over (S0h, kitsunium/sdk#738): each unit's actor ends
// between two events without stopping a process — its model written down,
// versioned, its processes, its descriptor store, an instance's connection
// and the notification socket ListenNotify made kept in the hand-over —, and
// the unit is closed: every later call, and every caller still waiting for
// a job, is UNIT_CLOSED. The jobs themselves are in the model and go on in
// the unit Resume makes. A unit closing or closed is UNIT_CLOSED and Freeze
// stops there: the hand-over holds the units frozen before it, which the
// caller resumes or closes. Off Linux it is UNSUPPORTED_PLATFORM.
func Freeze(ctx context.Context, units []*Unit) (*HandoverState, error) {
	//: no hand-over here.
	if err := handoverHere("Freeze"); err != nil {
		//: UNSUPPORTED_PLATFORM.
		return nil, err
	}
	h := NewHandoverState()
	//: each unit in turn.
	for _, u := range units {
		fu, err := u.freeze(ctx)
		//: refused: what was frozen stays the caller's.
		if err != nil {
			//: the units frozen so far, and the refusal.
			return h, err
		}
		h.units = append(h.units, fu)
	}
	//: every unit frozen.
	return h, nil
}

// freeze asks the actor to freeze the unit and waits for its end. Once the
// request is posted it is not withdrawn: a unit whose processes the actor
// forgot must reach the hand-over.
func (u *Unit) freeze(ctx context.Context) (*frozenUnit, error) {
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()
	//: a closed unit has nothing to hand.
	if closed {
		//: UNIT_CLOSED.
		return nil, unitClosed(u.s.name)
	}
	//: the caller gave up before anything was asked.
	if err := ctx.Err(); err != nil {
		//: its error.
		return nil, err
	}
	//: refused: closing, or closed meanwhile.
	if err := u.send(context.WithoutCancel(ctx), evFreeze); err != nil {
		//: the refusal.
		return nil, err
	}
	u.readers.Wait()
	u.mu.Lock()
	fu := u.frozen
	u.frozen = nil
	u.mu.Unlock()
	//: the unit as it was frozen.
	return fu, nil
}

// joinCause puts cause beside a sentinel in one chain, the sentinel first:
// errs.Wrap keeps the first *Error it finds as the origin, so the result is
// the sentinel's, errors.Is and errs.HasCode reaching the cause too.
func joinCause(sentinel, cause error) error {
	//: both, the sentinel first.
	return errors.Join(sentinel, cause)
}

// unitClosed is UNIT_CLOSED naming the unit.
func unitClosed(name string) error {
	//: the sentinel with the unit.
	return errs.Wrap(coresvc.UnitClosed, errs.WrapParams{}, errs.String("unit", name))
}

// Resume makes the unit cfg names from the state h holds for it, as NewUnit
// makes one from nothing: the model as it was frozen — its jobs, Restart=
// due, the start limiter's count, the store —, each process held again (a
// child through proc/exec's Reclaim, which reads its real status; an
// adopted one through cfg.Attacher, checked against the handle the outgoing
// image passed), each timer armed for its deadline, the group's and the bus
// name's watches begun again — the bus's first answer a lookup, never a
// loss —, the store's descriptors watched again, the notification socket
// kept at its path. A process that cannot be held again is read as gone,
// its status unknown. cfg is resolved as NewUnit resolves it, its refusals
// the same; a cfg.Notify is closed when the hand-over carries the socket.
// A state h does not hold, or cannot read, is HANDOVER_INVALID. Off Linux
// it is UNSUPPORTED_PLATFORM.
func Resume(cfg *Config, h *HandoverState) (*Unit, error) {
	own := *cfg
	//: no hand-over here.
	if err := handoverHere("Resume"); err != nil {
		closeNotify(own.Notify)
		//: UNSUPPORTED_PLATFORM.
		return nil, err
	}
	var fu *frozenUnit
	//: a hand-over to read.
	if h != nil {
		fu = h.unit(own.Name)
	}
	//: no state for the unit.
	if fu == nil {
		closeNotify(own.Notify)
		//: HANDOVER_INVALID naming the unit.
		return nil, handoverInvalid("no state for the unit", errs.String("unit", own.Name))
	}
	m, err := decodeModel(fu.model)
	//: a model this image cannot run: the hand-over keeps it, for Close.
	if err != nil {
		closeNotify(own.Notify)
		//: HANDOVER_INVALID naming the unit.
		return nil, errs.Wrap(err, errs.WrapParams{}, errs.String("unit", own.Name))
	}
	//: the socket the hand-over carries replaces the caller's, before the
	//: settings, which check a notify unit has one.
	if fu.notify != nil {
		src, aerr := adoptNotify(fu.notify, fu.socket)
		closeFiles([]*os.File{fu.notify})
		fu.notify = nil
		//: a socket kept: the unit's.
		if aerr == nil {
			closeNotify(own.Notify)
			own.Notify = src
		}
	}
	s, err := resolve(&own)
	//: a configuration the engine refuses: the hand-over keeps the rest.
	if err != nil {
		closeNotify(own.Notify)
		//: the refusal, naming the directive.
		return nil, err
	}
	fu.taken = true
	conn := fu.conn
	fu.conn = nil
	r := &restoring{fu: fu, m: m, ready: make(chan struct{}, 1)}
	u := launchRestored(&own, s, conn, r)
	<-r.ready
	//: the unit, as it was.
	return u, nil
}

// closeNotify closes a notification source nobody takes.
func closeNotify(src coresvc.NotifySource) {
	//: one to close.
	if src != nil {
		gone(src.Close())
	}
}

// launchRestored builds a resumed unit and starts its goroutines, its actor
// starting from r.
//
// Goroutine lifecycle: the actor and the notification reader, as NewUnit's.
func launchRestored(cfg *Config, s *settings, conn *os.File, r *restoring) *Unit {
	s.put(setInstance, conn != nil)
	u := newUnit(cfg, s)
	u.conn = conn
	u.restoring = r
	u.readers.Add(2)
	//: Goroutine lifecycle: one actor per unit, ended by Close (efExit),
	//: joined by Close through readers.
	go u.run(s)
	//: the notification reader, when the unit has a socket.
	if cfg.Notify != nil {
		//: Goroutine lifecycle: ended by Close, which cancels ctx and closes
		//: the source; joined by Close through readers.
		go u.readNotify(cfg.Notify)
	} else {
		u.readers.Done()
	}
	//: the unit, resumed.
	return u
}

// OpenImage opens the program a re-exec runs, O_PATH and close-on-exec,
// as a supervisor does at its start: the descriptor names that file
// whatever later happens to its path. Linux amd64 and arm64;
// UNSUPPORTED_PLATFORM elsewhere.
func OpenImage(path string) (*os.File, error) {
	//: the platform's open.
	return openImage(path)
}

// Reexec executes image — OpenImage's descriptor — again in this process,
// with argv and env, handing it h: the payload is written to a sealed
// memfd, every descriptor of the process is sealed close-on-exec, then,
// under syscall.ForkLock, close-on-exec is lifted on the payload and on
// h's descriptors alone and the image executed. The process keeps its PID,
// so every child stays its child. It returns only when the kernel refused:
// REEXEC_FAILED, the descriptors close-on-exec again, h unchanged — its
// units resumed or closed by the caller. Linux amd64 and arm64;
// UNSUPPORTED_PLATFORM elsewhere.
func Reexec(h *HandoverState, argv []string, image *os.File, env []string) error {
	//: nothing to hand, or nothing to run.
	if h == nil || image == nil || len(argv) == 0 {
		//: HANDOVER_INVALID.
		return handoverInvalid("no hand-over, image or argv")
	}
	//: the platform's exec.
	return reexec(h, image, argv, env)
}

// TakeHandover reads the hand-over the outgoing image left this one, once,
// before anything else spawns: nil when there is none. The payload's
// descriptor must be a memfd sealed whole, its version one this image
// reads, its sum right; every descriptor it names is checked by fstat —
// type, device, inode — and made close-on-exec again, and any other
// descriptor the exec left open is closed. A hand-over refused —
// HANDOVER_INVALID, HANDOVER_MISMATCH — leaves nothing behind: every
// descriptor it carried is closed and every child this process has is
// stopped in order (SIGTERM, grace, SIGKILL) and reaped, so none is left
// unsupervised; the pid field names each one stopped. Linux amd64 and
// arm64; nil elsewhere, where no image hands over.
func TakeHandover(ctx context.Context, grace time.Duration) (*HandoverState, error) {
	//: the platform's read.
	return takeHandover(ctx, grace)
}

// fileRecord is what the payload says of one descriptor: its number in the
// incoming image — the outgoing image's, kept by the exec — and what fstat
// read of it there.
type fileRecord struct {
	index, fd int
	mode      uint32
	dev, ino  uint64
}

// unitRefs are a decoded unit's references to the payload's descriptors, by
// index, before they are opened.
type unitRefs struct {
	fu       *frozenUnit
	store    map[uint64]int
	witness  map[int]int
	conn     int
	notify   int
	hasConn  bool
	hasNotif bool
}

// encode writes h's units no Resume took as a payload of version, and lists
// the descriptors it names, by index.
func (h *HandoverState) encode(version uint16) ([]byte, []*os.File, error) {
	w := &writer{}
	var files []*os.File
	add := func(f *os.File) uint64 {
		files = append(files, f)
		//: its index, from 1.
		return uint64(len(files))
	}
	//: each unit still held.
	for _, fu := range h.units {
		//: taken already.
		if fu.taken {
			continue
		}
		w.record(tagUnit, encodeUnit(fu, add))
	}
	//: each descriptor, with what fstat reads of it.
	for i, f := range files {
		rec, err := fileIdentity(f)
		//: a descriptor fstat cannot read cannot be checked.
		if err != nil {
			//: HANDOVER_MISMATCH.
			return nil, nil, err
		}
		fw := &writer{}
		fw.u64(fIndex, uint64(i+1))
		fw.i64(fFD, int64(rec.fd))
		fw.u64(fMode, uint64(rec.mode))
		fw.u64(fDev, rec.dev)
		fw.u64(fIno, rec.ino)
		w.record(tagFile, fw.b)
	}
	//: the payload and its descriptors.
	return seal(w.b, version), files, nil
}

// encodeUnit writes one frozen unit; add numbers each descriptor it names.
func encodeUnit(fu *frozenUnit, add func(*os.File) uint64) []byte {
	uw := &writer{}
	uw.str(uName, fu.name)
	uw.record(uModel, fu.model)
	//: each process.
	for _, p := range fu.procs {
		pw := &writer{}
		pw.i64(pPID, int64(p.pid))
		pw.flag(pChild, p.child)
		//: an adopted one's witness.
		if p.witness != nil {
			pw.u64(pWitness, add(p.witness))
		}
		uw.record(uProc, pw.b)
	}
	//: the store, by id.
	for _, id := range slices.Sorted(mapKeys(fu.store)) {
		sw := &writer{}
		sw.u64(sID, id)
		sw.u64(sFile, add(fu.store[id]))
		uw.record(uStore, sw.b)
	}
	//: an instance's connection.
	if fu.conn != nil {
		uw.u64(uConn, add(fu.conn))
	}
	//: the notification socket.
	if fu.notify != nil {
		uw.u64(uNotify, add(fu.notify))
		uw.str(uSocket, fu.socket)
	}
	//: the records.
	return uw.b
}

// decodeHandover reads a payload's units and its descriptors' records; the
// descriptors themselves are the platform's to open.
func decodeHandover(payload []byte) ([]*unitRefs, []fileRecord, error) {
	body, err := unseal(payload)
	//: not a payload this image reads.
	if err != nil {
		//: HANDOVER_INVALID.
		return nil, nil, err
	}
	var units []*unitRefs
	var files []fileRecord
	r := &reader{b: body}
	//: each record; one this image does not know is skipped.
	for tag, v, ok := r.next(); ok; tag, v, ok = r.next() {
		//: by tag.
		switch tag {
		case tagUnit:
			units = append(units, decodeUnit(r, v))
		case tagFile:
			files = append(files, decodeFile(r, v))
		default:
		}
	}
	//: a malformed record.
	if r.err != nil {
		//: HANDOVER_INVALID.
		return nil, nil, r.err
	}
	//: the references must name descriptors the payload lists.
	return units, files, checkRefs(units, files)
}

// unitFields reads each of a unit's records into ref.
var unitFields = map[uint16]func(ref *unitRefs, r *reader, v []byte){
	uName:   func(ref *unitRefs, _ *reader, v []byte) { ref.fu.name = string(v) },
	uModel:  func(ref *unitRefs, _ *reader, v []byte) { ref.fu.model = slices.Clone(v) },
	uProc:   func(ref *unitRefs, r *reader, v []byte) { ref.addProc(r, v) },
	uStore:  func(ref *unitRefs, r *reader, v []byte) { ref.addStored(r, v) },
	uConn:   func(ref *unitRefs, r *reader, v []byte) { ref.conn, ref.hasConn = r.int(v), true },
	uNotify: func(ref *unitRefs, r *reader, v []byte) { ref.notify, ref.hasNotif = r.int(v), true },
	uSocket: func(ref *unitRefs, _ *reader, v []byte) { ref.fu.socket = string(v) },
}

// decodeUnit reads one unit's records.
func decodeUnit(r *reader, b []byte) *unitRefs {
	ref := &unitRefs{fu: &frozenUnit{store: map[uint64]*os.File{}}, store: map[uint64]int{}, witness: map[int]int{}}
	sub := &reader{b: b}
	//: each record; one this image does not know is skipped.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: a known field.
		if read := unitFields[tag]; read != nil {
			read(ref, sub, v)
		}
	}
	r.keep(sub.err)
	//: the unit, its descriptors by index.
	return ref
}

// addProc reads one process.
func (u *unitRefs) addProc(r *reader, b []byte) {
	var p handedProc
	witness := 0
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case pPID:
			p.pid = sub.int(v)
		case pChild:
			p.child = sub.u64(v) != 0
		case pWitness:
			witness = sub.int(v)
		default:
		}
	}
	r.keep(sub.err)
	//: a witness to open.
	if witness != 0 {
		u.witness[len(u.fu.procs)] = witness
	}
	u.fu.procs = append(u.fu.procs, p)
}

// addStored reads one stored descriptor's reference.
func (u *unitRefs) addStored(r *reader, b []byte) {
	var id uint64
	index := 0
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case sID:
			id = sub.u64(v)
		case sFile:
			index = sub.int(v)
		default:
		}
	}
	r.keep(sub.err)
	u.store[id] = index
}

// decodeFile reads one descriptor's record.
func decodeFile(r *reader, b []byte) fileRecord {
	var f fileRecord
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case fIndex:
			f.index = sub.int(v)
		case fFD:
			f.fd = sub.int(v)
		case fMode:
			f.mode = uint32(sub.u64(v))
		case fDev:
			f.dev = sub.u64(v)
		case fIno:
			f.ino = sub.u64(v)
		default:
		}
	}
	r.keep(sub.err)
	//: the record.
	return f
}

// checkRefs refuses units without a name or a process without a pid, and
// references to descriptors the payload does not list, or listed twice.
func checkRefs(units []*unitRefs, files []fileRecord) error {
	seen, err := checkRecords(files)
	//: a descriptor named twice, or at stdio.
	if err != nil {
		//: refused.
		return err
	}
	//: each unit's references.
	for _, ref := range units {
		//: a name, pids and indices the payload lists.
		if err = checkUnitRefs(ref, seen); err != nil {
			//: refused.
			return err
		}
	}
	//: consistent.
	return nil
}

// checkRecords refuses a descriptor listed twice or at stdio, and returns the
// indices listed.
func checkRecords(files []fileRecord) (map[int]bool, error) {
	seen := map[int]bool{}
	fds := map[int]bool{}
	//: each descriptor, once, past stdio.
	for _, f := range files {
		//: an index or a number repeated, or stdio.
		if f.index < 1 || seen[f.index] || fds[f.fd] || f.fd < firstPassedFD {
			//: refused.
			return nil, handoverInvalid("a descriptor listed twice, or at stdio", errs.Int("fd", f.fd))
		}
		seen[f.index], fds[f.fd] = true, true
	}
	//: the indices.
	return seen, nil
}

// checkUnitRefs refuses a unit without a name, a process without a pid, or
// an index the payload does not list.
func checkUnitRefs(ref *unitRefs, seen map[int]bool) error {
	//: no name, or a process without a pid.
	if ref.fu.name == "" || slices.ContainsFunc(ref.fu.procs, func(p handedProc) bool { return p.pid < 1 }) {
		//: refused.
		return handoverInvalid("a unit without a name, or a process without a pid")
	}
	named := slices.AppendSeq(slices.Collect(maps.Values(ref.witness)), maps.Values(ref.store))
	//: the connection.
	if ref.hasConn {
		named = append(named, ref.conn)
	}
	//: the socket.
	if ref.hasNotif {
		named = append(named, ref.notify)
	}
	//: each index it names.
	for _, i := range named {
		//: one the payload does not list.
		if !seen[i] {
			//: refused.
			return handoverInvalid("a descriptor the payload does not list", errs.String("unit", ref.fu.name))
		}
	}
	//: consistent.
	return nil
}

// resolveRefs gives each unit the descriptors its indices name, from the
// opened ones by index; h holds them from here on.
func resolveRefs(units []*unitRefs, opened map[int]*os.File) *HandoverState {
	h := NewHandoverState()
	//: each unit.
	for _, ref := range units {
		fu := ref.fu
		//: the witnesses.
		for i, idx := range ref.witness {
			fu.procs[i].witness = opened[idx]
		}
		//: the store.
		for id, idx := range ref.store {
			fu.store[id] = opened[idx]
		}
		//: the connection.
		if ref.hasConn {
			fu.conn = opened[ref.conn]
		}
		//: the socket.
		if ref.hasNotif {
			fu.notify = opened[ref.notify]
		}
		h.units = append(h.units, fu)
	}
	//: the hand-over.
	return h
}
