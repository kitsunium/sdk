package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The hand-over's wire form (S0h, kitsunium/sdk#738): what an outgoing image
// writes of each unit and what the incoming one reads. A payload is
//
//	magic (8) | version (2) | body length (4) | body | sha256(body) (32)
//
// and the body a list of records, each a tag (2), a length (4) and its value,
// little-endian. A tag an image does not know is skipped — a field a later
// image added — and a field an earlier image did not write is read as its
// zero: within one version an image reads what an older or a newer one
// wrote. A change that cannot be read that way takes a new version, and an
// image refuses a version it does not know (HANDOVER_INVALID).

const (
	// handoverVersion is the version this image writes, and the newest it
	// reads.
	handoverVersion uint16 = 1
	// handoverMagic opens every payload.
	handoverMagic string = "KSDKHOV1"
	// maxHandoverBytes bounds a payload: read whole, before it is trusted.
	maxHandoverBytes int = 16 << 20
	// recordHeader is a record's tag and length.
	recordHeader int = 6
	// payloadHeader is the magic, the version and the body's length.
	payloadHeader int = 14
)

// The payload's records.
const (
	// tagUnit is one unit: its own records.
	tagUnit uint16 = iota + 1
	// tagFile is one descriptor the payload names: its own records.
	tagFile
)

// A unit's records.
const (
	uName uint16 = iota + 1
	uModel
	uProc
	uStore
	uConn
	uNotify
	uSocket
)

// A process's, a stored descriptor's and a file's records.
const (
	pPID uint16 = iota + 1
	pChild
	pWitness
)

// A stored descriptor's records.
const (
	sID uint16 = iota + 1
	sFile
)

// A descriptor's records.
const (
	fIndex uint16 = iota + 1
	fFD
	fMode
	fDev
	fIno
)

// The model's records: every field of the model but its settings, which the
// incoming image resolves again from Config, and its waiters, who were the
// outgoing image's callers.
const (
	mChanged uint16 = iota + 1
	mLimitBegin
	mRuntimeAt
	mRevalidateBegin
	mDeadlineState
	mDeadlineWatchdog
	mDeadlinePIDFile
	mPublishedAt
	mStatus
	mBusError
	mVarlinkError
	mExitStatusText
	mPIDFileCause
	mJob
	mPending
	mStoreEntry
	mStoreKey
	mSandboxField
	mSandboxCause
	mMainExit
	mSeqState
	mSeqWatchdog
	mSeqPIDFile
	mGeneration
	mNotifyRefused
	mReloadBegin
	mStoreSeq
	mStoreRefused
	mStoreDiscarded
	mWatchdogPeriod
	mMainPID
	mControlPID
	mCmd
	mErrno
	mLimitCount
	mRestarts
	mState
	mSub
	mResult
	mReloadResult
	mNotifyAccess
	mNotify
	mList
	mMainFlags
	mFlags
)

// A job's, a store entry's, a field's and an exit's records.
const (
	jKind uint16 = iota + 1
	jStopped
)

// A store entry's records.
const (
	eName uint16 = iota + 1
	eID
	ePoll
)

// A sandbox failure field's records.
const (
	kKey uint16 = iota + 1
	kValue
)

// An exit's records.
const (
	xCode uint16 = iota + 1
	xSignal
	xSignaled
	xUser
	xSystem
	xMaxRSS
	xCore
	xUsage
)

// frozenFlags are the model's flags a frozen unit may carry: Close's and the
// end's are not — a closing unit is not handed over.
const frozenFlags = flagMainExited | flagForbidRestart | flagRestarting | flagDebug |
	flagCgroupGood | flagRan | flagGroupPopulated | flagPrepare | flagBusOwned

// writer builds records.
type writer struct {
	b []byte
}

// record appends one record.
func (w *writer) record(tag uint16, v []byte) {
	w.b = binary.LittleEndian.AppendUint16(w.b, tag)
	w.b = binary.LittleEndian.AppendUint32(w.b, uint32(len(v)))
	w.b = append(w.b, v...)
}

// u64 appends an unsigned record.
func (w *writer) u64(tag uint16, v uint64) {
	w.record(tag, binary.LittleEndian.AppendUint64(nil, v))
}

// i64 appends a signed record.
func (w *writer) i64(tag uint16, v int64) {
	w.u64(tag, uint64(v))
}

// flag appends a boolean, only when it is set: absent is false.
func (w *writer) flag(tag uint16, on bool) {
	//: set.
	if on {
		w.u64(tag, 1)
	}
}

// str appends a string, only when it is not empty.
func (w *writer) str(tag uint16, s string) {
	//: not empty.
	if s != "" {
		w.record(tag, []byte(s))
	}
}

// at appends a time, only when it is not zero: absent is the zero time.
func (w *writer) at(tag uint16, t time.Time) {
	//: a time.
	if !t.IsZero() {
		w.i64(tag, t.UnixNano())
	}
}

// reader walks records.
type reader struct {
	b   []byte
	err error
}

// next returns the next record; false at the end or on a malformed one.
func (r *reader) next() (uint16, []byte, bool) {
	//: the end, or a failure already.
	if len(r.b) == 0 || r.err != nil {
		//: nothing.
		return 0, nil, false
	}
	//: a header cut short.
	if len(r.b) < recordHeader {
		r.err = handoverInvalid("a record cut short")
		//: nothing.
		return 0, nil, false
	}
	tag := binary.LittleEndian.Uint16(r.b)
	n := binary.LittleEndian.Uint32(r.b[2:])
	//: a length past the end.
	if uint64(n) > uint64(len(r.b)-recordHeader) {
		r.err = handoverInvalid("a record past the payload's end")
		//: nothing.
		return 0, nil, false
	}
	v := r.b[recordHeader : recordHeader+int(n)]
	r.b = r.b[recordHeader+int(n):]
	//: the record.
	return tag, v, true
}

// u64 reads an unsigned value.
func (r *reader) u64(v []byte) uint64 {
	//: eight bytes, nothing else.
	if len(v) != 8 {
		r.err = handoverInvalid("an integer of the wrong width")
		//: zero.
		return 0
	}
	//: the value.
	return binary.LittleEndian.Uint64(v)
}

// i64 reads a signed value.
func (r *reader) i64(v []byte) int64 {
	//: the bits.
	return int64(r.u64(v))
}

// int reads a value that fits an int.
func (r *reader) int(v []byte) int {
	n := r.i64(v)
	//: past an int.
	if int64(int(n)) != n {
		r.err = handoverInvalid("an integer past an int")
		//: zero.
		return 0
	}
	//: the value.
	return int(n)
}

// at reads a time.
func (r *reader) at(v []byte) time.Time {
	//: the instant.
	return time.Unix(0, r.i64(v))
}

// str is a record's text.
func (r *reader) str(v []byte) string {
	//: a copy of the bytes.
	return string(v)
}

// handoverInvalid is HANDOVER_INVALID saying why.
func handoverInvalid(why string, fields ...errs.FieldValue) error {
	//: the sentinel with the reason.
	return errs.Wrap(coresvc.HandoverInvalid, errs.WrapParams{}, append([]errs.FieldValue{errs.String("why", why)}, fields...)...)
}

// seal frames a body as a payload of version.
func seal(body []byte, version uint16) []byte {
	out := make([]byte, 0, payloadHeader+len(body)+sha256.Size)
	out = append(out, handoverMagic...)
	out = binary.LittleEndian.AppendUint16(out, version)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	out = append(out, body...)
	sum := sha256.Sum256(body)
	//: framed and summed.
	return append(out, sum[:]...)
}

// unseal checks a payload's frame and returns its body: the magic, a version
// this image reads, the length, the sum. Anything else is HANDOVER_INVALID.
func unseal(payload []byte) ([]byte, error) {
	//: too short for a frame, or too long to be read.
	if len(payload) < payloadHeader+sha256.Size || len(payload) > maxHandoverBytes {
		//: refused.
		return nil, handoverInvalid("a payload of no possible length", errs.Int("bytes", len(payload)))
	}
	//: not a hand-over.
	if string(payload[:len(handoverMagic)]) != handoverMagic {
		//: refused.
		return nil, handoverInvalid("no hand-over magic")
	}
	version := binary.LittleEndian.Uint16(payload[8:])
	//: a version this image does not know: a later one, or none.
	if version == 0 || version > handoverVersion {
		//: refused, naming it.
		return nil, handoverInvalid("a version this image does not read", errs.Int("version", int(version)), errs.Int("reads", int(handoverVersion)))
	}
	n := binary.LittleEndian.Uint32(payload[10:])
	//: the length does not frame the rest.
	if uint64(n) != uint64(len(payload)-payloadHeader-sha256.Size) {
		//: refused.
		return nil, handoverInvalid("a body length that does not match")
	}
	body := payload[payloadHeader : payloadHeader+int(n)]
	sum := sha256.Sum256(body)
	//: a body that is not the one summed.
	if !bytes.Equal(sum[:], payload[payloadHeader+int(n):]) {
		//: refused.
		return nil, handoverInvalid("a body that does not match its sum")
	}
	//: the body.
	return body, nil
}

// encodeModel writes a frozen unit's model; its waiters are not written.
func encodeModel(m *model) []byte {
	w := &writer{}
	encodeTimes(w, m)
	encodeTexts(w, m)
	encodeStored(w, m)
	w.record(mMainExit, encodeExit(&m.mainExit))
	encodeCounters(w, m)
	encodeSteps(w, m)
	//: the records.
	return w.b
}

// encodeTimes writes the model's instants.
func encodeTimes(w *writer, m *model) {
	w.at(mChanged, m.changed)
	w.at(mLimitBegin, m.limitBegin)
	w.at(mRuntimeAt, m.runtimeAt)
	w.at(mRevalidateBegin, m.revalidateBegin)
	w.at(mDeadlineState, m.deadlines[timerState])
	w.at(mDeadlineWatchdog, m.deadlines[timerWatchdog])
	w.at(mDeadlinePIDFile, m.deadlines[timerPIDFile])
	w.at(mPublishedAt, m.published.At)
}

// encodeTexts writes the model's texts and jobs.
func encodeTexts(w *writer, m *model) {
	w.str(mStatus, m.status)
	w.str(mBusError, m.busError)
	w.str(mVarlinkError, m.varlinkError)
	w.str(mExitStatusText, m.exitStatusText)
	w.str(mPIDFileCause, m.pidFileCause)
	w.record(mJob, encodeJob(m.job))
	w.record(mPending, encodeJob(m.pending))
}

// encodeStored writes the store's entries and a sandbox failure's fields.
func encodeStored(w *writer, m *model) {
	//: each stored descriptor, oldest first.
	for _, e := range m.store {
		ew := &writer{}
		ew.str(eName, e.name)
		ew.u64(eID, e.id)
		ew.flag(ePoll, e.poll)
		w.record(mStoreEntry, ew.b)
	}
	w.str(mStoreKey, m.storeKey)
	//: each field of a sandbox's failure, as text.
	for _, f := range m.sandboxFields {
		fw := &writer{}
		fw.str(kKey, f.Key())
		fw.record(kValue, []byte(f.StringValue()))
		w.record(mSandboxField, fw.b)
	}
	w.u64(mSandboxCause, uint64(m.sandboxCause))
}

// encodeCounters writes the model's numbers.
func encodeCounters(w *writer, m *model) {
	w.u64(mSeqState, m.seq[timerState])
	w.u64(mSeqWatchdog, m.seq[timerWatchdog])
	w.u64(mSeqPIDFile, m.seq[timerPIDFile])
	w.u64(mGeneration, m.generation)
	w.u64(mNotifyRefused, m.notifyRefused)
	w.u64(mReloadBegin, m.reloadBegin)
	w.u64(mStoreSeq, m.storeSeq)
	w.u64(mStoreRefused, m.storeRefused)
	w.u64(mStoreDiscarded, m.storeDiscarded)
	w.i64(mWatchdogPeriod, int64(m.watchdogPeriod))
	w.i64(mMainPID, int64(m.mainPID))
	w.i64(mControlPID, int64(m.controlPID))
	w.i64(mCmd, int64(m.cmd))
	w.i64(mErrno, int64(m.errno))
	w.i64(mLimitCount, int64(m.limitCount))
	w.u64(mRestarts, uint64(m.restarts))
}

// encodeSteps writes the model's enumerated values and flags.
func encodeSteps(w *writer, m *model) {
	w.u64(mState, uint64(m.state))
	w.u64(mSub, uint64(m.sub))
	w.u64(mResult, uint64(m.result))
	w.u64(mReloadResult, uint64(m.reloadResult))
	w.u64(mNotifyAccess, uint64(m.notifyAccess))
	w.u64(mNotify, uint64(m.notify))
	w.u64(mList, uint64(m.list))
	w.u64(mMainFlags, uint64(m.mainFlags))
	w.u64(mFlags, uint64(m.flags&frozenFlags))
}

// encodeJob writes a job without its waiters.
func encodeJob(j job) []byte {
	w := &writer{}
	w.u64(jKind, uint64(j.kind))
	w.flag(jStopped, j.stopped)
	//: the records.
	return w.b
}

// encodeExit writes an exit.
func encodeExit(x *coreproc.ExitValue) []byte {
	w := &writer{}
	w.i64(xCode, int64(x.Code))
	w.i64(xSignal, int64(x.Signal))
	w.flag(xSignaled, x.Signaled)
	w.i64(xUser, int64(x.UserTime))
	w.i64(xSystem, int64(x.SystemTime))
	w.i64(xMaxRSS, x.MaxRSS)
	w.flag(xCore, x.CoreDumped)
	//: the counters, when the kernel filled them.
	if u := x.Usage; u.Known {
		uw := &writer{}
		//: in their order.
		for i, v := range []uint64{u.MinorFaults, u.MajorFaults, u.InBlock, u.OutBlock, u.VoluntaryCtxSw, u.InvoluntaryCtxSw} {
			uw.u64(uint16(i+1), v)
		}
		w.record(xUsage, uw.b)
	}
	//: the records.
	return w.b
}

// The record tables: what each tag writes into the value being read.
var (
	// modelFields reads each of the model's scalar records into m.
	modelFields = map[uint16]func(r *reader, m *model, v []byte){
		mChanged:          func(r *reader, m *model, v []byte) { m.changed = r.at(v) },
		mLimitBegin:       func(r *reader, m *model, v []byte) { m.limitBegin = r.at(v) },
		mRuntimeAt:        func(r *reader, m *model, v []byte) { m.runtimeAt = r.at(v) },
		mRevalidateBegin:  func(r *reader, m *model, v []byte) { m.revalidateBegin = r.at(v) },
		mDeadlineState:    func(r *reader, m *model, v []byte) { m.deadlines[timerState] = r.at(v) },
		mDeadlineWatchdog: func(r *reader, m *model, v []byte) { m.deadlines[timerWatchdog] = r.at(v) },
		mDeadlinePIDFile:  func(r *reader, m *model, v []byte) { m.deadlines[timerPIDFile] = r.at(v) },
		mPublishedAt:      func(r *reader, m *model, v []byte) { m.published.At = r.at(v) },
		mStatus:           func(_ *reader, m *model, v []byte) { m.status = string(v) },
		mBusError:         func(_ *reader, m *model, v []byte) { m.busError = string(v) },
		mVarlinkError:     func(_ *reader, m *model, v []byte) { m.varlinkError = string(v) },
		mExitStatusText:   func(_ *reader, m *model, v []byte) { m.exitStatusText = string(v) },
		mPIDFileCause:     func(_ *reader, m *model, v []byte) { m.pidFileCause = string(v) },
		mJob:              func(r *reader, m *model, v []byte) { m.job = decodeJob(r, v) },
		mPending:          func(r *reader, m *model, v []byte) { m.pending = decodeJob(r, v) },
		mStoreEntry:       func(r *reader, m *model, v []byte) { m.store = append(m.store, decodeEntry(r, v)) },
		mStoreKey:         func(_ *reader, m *model, v []byte) { m.storeKey = string(v) },
		mSandboxField:     func(r *reader, m *model, v []byte) { m.sandboxFields = append(m.sandboxFields, decodeField(r, v)) },
		mSandboxCause:     func(r *reader, m *model, v []byte) { m.sandboxCause = errs.Code(r.u64(v)) },
		mMainExit:         func(r *reader, m *model, v []byte) { m.mainExit = decodeExit(r, v) },
		mSeqState:         func(r *reader, m *model, v []byte) { m.seq[timerState] = r.u64(v) },
		mSeqWatchdog:      func(r *reader, m *model, v []byte) { m.seq[timerWatchdog] = r.u64(v) },
		mSeqPIDFile:       func(r *reader, m *model, v []byte) { m.seq[timerPIDFile] = r.u64(v) },
		mGeneration:       func(r *reader, m *model, v []byte) { m.generation = r.u64(v) },
		mNotifyRefused:    func(r *reader, m *model, v []byte) { m.notifyRefused = r.u64(v) },
		mReloadBegin:      func(r *reader, m *model, v []byte) { m.reloadBegin = r.u64(v) },
		mStoreSeq:         func(r *reader, m *model, v []byte) { m.storeSeq = r.u64(v) },
		mStoreRefused:     func(r *reader, m *model, v []byte) { m.storeRefused = r.u64(v) },
		mStoreDiscarded:   func(r *reader, m *model, v []byte) { m.storeDiscarded = r.u64(v) },
		mWatchdogPeriod:   func(r *reader, m *model, v []byte) { m.watchdogPeriod = time.Duration(r.i64(v)) },
		mMainPID:          func(r *reader, m *model, v []byte) { m.mainPID = r.int(v) },
		mControlPID:       func(r *reader, m *model, v []byte) { m.controlPID = r.int(v) },
		mCmd:              func(r *reader, m *model, v []byte) { m.cmd = r.int(v) },
		mErrno:            func(r *reader, m *model, v []byte) { m.errno = r.int(v) },
		mLimitCount:       func(r *reader, m *model, v []byte) { m.limitCount = r.int(v) },
		mRestarts:         func(r *reader, m *model, v []byte) { m.restarts = uint32(r.u64(v)) },
		mState:            func(r *reader, m *model, v []byte) { m.state = coresvc.State(r.u64(v)) },
		mSub:              func(r *reader, m *model, v []byte) { m.sub = coresvc.Sub(r.u64(v)) },
		mResult:           func(r *reader, m *model, v []byte) { m.result = coresvc.Result(r.u64(v)) },
		mReloadResult:     func(r *reader, m *model, v []byte) { m.reloadResult = coresvc.Result(r.u64(v)) },
		mNotifyAccess:     func(r *reader, m *model, v []byte) { m.notifyAccess = coresvc.NotifyAccess(r.u64(v)) },
		mNotify:           func(r *reader, m *model, v []byte) { m.notify = notifyState(r.u64(v)) },
		mList:             func(r *reader, m *model, v []byte) { m.list = list(r.u64(v)) },
		mMainFlags:        func(r *reader, m *model, v []byte) { m.mainFlags = coresvc.CommandFlags(r.u64(v)) },
		mFlags:            func(r *reader, m *model, v []byte) { m.flags = modelFlag(r.u64(v)) },
	}

	// exitFields reads each of an exit's records into x.
	exitFields = map[uint16]func(x *coreproc.ExitValue, r *reader, v []byte){
		xCode:     func(x *coreproc.ExitValue, r *reader, v []byte) { x.Code = r.int(v) },
		xSignal:   func(x *coreproc.ExitValue, r *reader, v []byte) { x.Signal = coreproc.Signal(r.int(v)) },
		xSignaled: func(x *coreproc.ExitValue, r *reader, v []byte) { x.Signaled = r.u64(v) != 0 },
		xUser:     func(x *coreproc.ExitValue, r *reader, v []byte) { x.UserTime = time.Duration(r.i64(v)) },
		xSystem:   func(x *coreproc.ExitValue, r *reader, v []byte) { x.SystemTime = time.Duration(r.i64(v)) },
		xMaxRSS:   func(x *coreproc.ExitValue, r *reader, v []byte) { x.MaxRSS = r.i64(v) },
		xCore:     func(x *coreproc.ExitValue, r *reader, v []byte) { x.CoreDumped = r.u64(v) != 0 },
		xUsage:    func(x *coreproc.ExitValue, r *reader, v []byte) { x.Usage = decodeUsage(r, v) },
	}
)

// decodeModel reads a model a unit was frozen with, then checks every value
// is one this image runs: HANDOVER_INVALID otherwise. Its settings are the
// caller's to set.
func decodeModel(b []byte) (model, error) {
	var m model
	r := &reader{b: b}
	//: each record; one this image does not know is skipped.
	for tag, v, ok := r.next(); ok; tag, v, ok = r.next() {
		//: a known field.
		if read := modelFields[tag]; read != nil {
			read(r, &m, v)
		}
	}
	//: a malformed record.
	if r.err != nil {
		//: refused.
		return model{}, r.err
	}
	//: the values.
	return m, checkModel(&m)
}

// checkModel refuses a decoded model holding a value this image does not
// define, or flags a frozen unit cannot carry.
func checkModel(m *model) error {
	//: each enumerated value, by its spelling.
	for _, name := range []string{
		m.state.String(), m.sub.String(), m.result.String(), m.reloadResult.String(), m.notifyAccess.String(),
	} {
		//: an undefined value spells itself "kind(n)".
		if strings.Contains(name, "(") {
			//: refused.
			return handoverInvalid("a value this image does not define", errs.String("value", name))
		}
	}
	//: the steps, then the store's ids.
	return errors.Join(checkSteps(m), checkStoreIDs(m))
}

// checkSteps refuses a list, a job, a notification state or a flag past what
// this image has.
func checkSteps(m *model) error {
	//: a step this image does not define.
	if m.list >= listCount || m.job.kind > jobReload || m.pending.kind > jobReload || m.notify > notifyStopping || m.flags&^frozenFlags != 0 {
		//: refused.
		return handoverInvalid("a step this image does not define")
	}
	//: defined.
	return nil
}

// checkStoreIDs refuses a store whose ids repeat, or one past the sequence.
func checkStoreIDs(m *model) error {
	//: each entry.
	for i, e := range m.store {
		//: the same id twice, or one never handed out.
		if e.id == 0 || e.id > m.storeSeq || slices.ContainsFunc(m.store[:i], func(o storeEntry) bool { return o.id == e.id }) {
			//: refused.
			return handoverInvalid("a store whose ids do not hold")
		}
	}
	//: consistent.
	return nil
}

// decodeJob reads a job, without waiters.
func decodeJob(r *reader, b []byte) job {
	var j job
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case jKind:
			j.kind = jobKind(sub.u64(v))
		case jStopped:
			j.stopped = sub.u64(v) != 0
		default:
		}
	}
	r.keep(sub.err)
	//: the job.
	return j
}

// decodeEntry reads a store entry.
func decodeEntry(r *reader, b []byte) storeEntry {
	var e storeEntry
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case eName:
			e.name = sub.str(v)
		case eID:
			e.id = sub.u64(v)
		case ePoll:
			e.poll = sub.u64(v) != 0
		default:
		}
	}
	r.keep(sub.err)
	//: the entry.
	return e
}

// decodeField reads a field of a sandbox's failure, as text.
func decodeField(r *reader, b []byte) errs.FieldValue {
	var key, val string
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: by tag.
		switch tag {
		case kKey:
			key = sub.str(v)
		case kValue:
			val = sub.str(v)
		default:
		}
	}
	r.keep(sub.err)
	//: the field.
	return errs.String(key, val)
}

// decodeExit reads an exit.
func decodeExit(r *reader, b []byte) coreproc.ExitValue {
	var x coreproc.ExitValue
	sub := &reader{b: b}
	//: each record; one this image does not know is skipped.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: a known field.
		if read := exitFields[tag]; read != nil {
			read(&x, sub, v)
		}
	}
	r.keep(sub.err)
	//: the exit.
	return x
}

// decodeUsage reads an exit's counters.
func decodeUsage(r *reader, b []byte) coreproc.UsageValue {
	//: a record present is a usage the kernel filled.
	u := coreproc.UsageValue{Known: true}
	slots := [...]*uint64{&u.MinorFaults, &u.MajorFaults, &u.InBlock, &u.OutBlock, &u.VoluntaryCtxSw, &u.InvoluntaryCtxSw}
	sub := &reader{b: b}
	//: each record.
	for tag, v, ok := sub.next(); ok; tag, v, ok = sub.next() {
		//: a counter this image knows.
		if tag >= 1 && int(tag) <= len(slots) {
			*slots[tag-1] = sub.u64(v)
		}
	}
	r.keep(sub.err)
	//: the counters.
	return u
}

// keep takes a nested reader's failure as this one's.
func (r *reader) keep(err error) {
	//: the first failure stands.
	if r.err == nil {
		r.err = err
	}
}
