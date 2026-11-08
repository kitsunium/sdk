//go:build linux && (amd64 || arm64)

package cgroup

import (
	"errors"
	"slices"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corebpf "github.com/kitsunium/sdk/internal/core/proc/bpf"
)

// license is the programs' license string: they call no GPL-only helper.
const license = "MIT"

// cgroupTypeFile is a file every cgroup v2 group has but a hierarchy's
// root: its absence is how the root — of the machine, or of a cgroup
// namespace — is recognised and refused.
const cgroupTypeFile = "cgroup.type"

// groupOpenFlags open a group's directory to attach to: a directory, read
// only, never inherited by a child.
const groupOpenFlags int = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC

// memlock is the process's one memlocker, over uname(2) and setrlimit(2).
var memlock = &memlocker{release: unameRelease, raise: raiseMemlock}

// probe loads, then releases, the smallest cgroup_device program.
func probe() error {
	//: the limit first: a refused raise refuses every load.
	if err := memlock.remove(); err != nil {
		//: LOAD_REFUSED.
		return err
	}
	prog, err := load(ebpf.CGroupDevice, ebpf.AttachCGroupDevice, asm.Instructions{
		asm.Mov.Imm(asm.R0, retAllow),
		asm.Return(),
	})
	//: no CAP_BPF, or a kernel without cgroup_device.
	if err != nil {
		//: LOAD_REFUSED.
		return err
	}
	//: nothing was attached: closing the descriptor releases the program.
	if err := prog.Close(); err != nil {
		//: PROGRAM_FAILED.
		return programFailed(err, "close")
	}
	//: this process may load cgroup programs.
	return nil
}

// attachDevice loads and attaches one cgroup_device program.
func attachDevice(cgroup string, policy corebpf.DevicePolicyValue) (corebpf.Program, error) {
	h, err := open(cgroup)
	//: no group to attach to, or no right to load.
	if err != nil {
		//: ATTACH_FAILED or LOAD_REFUSED.
		return nil, err
	}
	//: the one program, on the one hook, or nothing left behind.
	if err := h.add(ebpf.CGroupDevice, ebpf.AttachCGroupDevice, deviceInstructions(policy)); err != nil {
		//: LOAD_REFUSED or ATTACH_FAILED.
		return nil, h.abandon(err)
	}
	//: attached.
	return h, nil
}

// attachSKB loads the maps and the two cgroup_skb programs, then attaches
// them, ingress first.
func attachSKB(cgroup string, policy corebpf.SKBPolicyValue) (corebpf.SKBProgram, error) {
	h, err := open(cgroup)
	//: no group to attach to, or no right to load.
	if err != nil {
		//: ATTACH_FAILED or LOAD_REFUSED.
		return nil, err
	}
	s := &skbHandle{handle: h}
	maps, err := s.makeMaps(policy)
	//: the maps, filled before any program can read them.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, h.abandon(err)
	}
	//: one program per hook, sharing the maps.
	for _, hook := range []struct {
		attach ebpf.AttachType
		dir    direction
	}{
		{ebpf.AttachCGroupInetIngress, ingress},
		{ebpf.AttachCGroupInetEgress, egress},
	} {
		insns, err := skbProgram(hook.dir, policy.Default, maps)
		//: a reference no map answers.
		if err != nil {
			//: LOAD_REFUSED.
			return nil, h.abandon(err)
		}
		//: loaded and attached, or the pair undone.
		if err := h.add(ebpf.CGroupSKB, hook.attach, insns); err != nil {
			//: LOAD_REFUSED or ATTACH_FAILED.
			return nil, h.abandon(err)
		}
	}
	//: attached.
	return s, nil
}

// attachBind loads and attaches a cgroup_sock_addr program on each bind
// hook, IPv4 first.
func attachBind(cgroup string, policy corebpf.BindPolicyValue) (corebpf.Program, error) {
	h, err := open(cgroup)
	//: no group to attach to, or no right to load.
	if err != nil {
		//: ATTACH_FAILED or LOAD_REFUSED.
		return nil, err
	}
	//: one program per family, each over the rules of its family.
	for _, hook := range []struct {
		attach ebpf.AttachType
		family corebpf.Family
	}{
		{ebpf.AttachCGroupInet4Bind, corebpf.FamilyIPv4},
		{ebpf.AttachCGroupInet6Bind, corebpf.FamilyIPv6},
	} {
		//: loaded and attached, or the pair undone.
		if err := h.add(ebpf.CGroupSockAddr, hook.attach, bindInstructions(hook.family, policy)); err != nil {
			//: LOAD_REFUSED or ATTACH_FAILED.
			return nil, h.abandon(err)
		}
	}
	//: attached.
	return h, nil
}

// load creates a program of a type for an attach type.
func load(typ ebpf.ProgramType, attach ebpf.AttachType, insns asm.Instructions) (*ebpf.Program, error) {
	spec := &ebpf.ProgramSpec{
		Type:         typ,
		AttachType:   attach,
		Instructions: insns,
		License:      license,
	}
	prog, err := ebpf.NewProgram(spec)
	//: the kernel's refusal, the verifier's log in the chain when it has one.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, loadRefused(err, "program")
	}
	//: loaded.
	return prog, nil
}

// handle is a group's attached programs and the maps they read, and the
// group's directory, held from the first attachment to Close.
type handle struct {
	mu     sync.Mutex
	dir    int
	path   string
	parts  []attached
	maps   []*ebpf.Map
	closed bool
}

// attached is one program on one hook.
type attached struct {
	prog   *ebpf.Program
	attach ebpf.AttachType
}

// open checks the limit, then opens the group's directory and refuses one
// that is not a cgroup v2 group, or that is its hierarchy's root.
func open(cgroup string) (*handle, error) {
	//: the limit first: a refused raise refuses every load.
	if err := memlock.remove(); err != nil {
		//: LOAD_REFUSED.
		return nil, err
	}
	fd, err := unix.Open(cgroup, groupOpenFlags, 0)
	//: a group that is not there, not a directory, or not ours to read.
	if err != nil {
		//: ATTACH_FAILED.
		return nil, attachFailed(err, cgroup, "open")
	}
	h := &handle{dir: fd, path: cgroup}
	//: the directory must be a group below a root.
	if step, err := checkGroup(fd); err != nil {
		//: ATTACH_FAILED, the directory closed.
		return nil, h.abandon(attachFailed(err, cgroup, step))
	}
	//: the directory, held until Close.
	return h, nil
}

// checkGroup refuses a directory that is not on cgroup2fs, or that is the
// root of its hierarchy, and names the check that failed.
func checkGroup(dir int) (string, error) {
	var fs unix.Statfs_t
	//: the filesystem the directory is on.
	if err := unix.Fstatfs(dir, &fs); err != nil {
		//: unreadable.
		return "statfs", err
	}
	//: cgroup v2 only.
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		//: not a cgroup v2 directory.
		return "not-cgroup2", unix.ENOTDIR
	}
	var st unix.Stat_t
	//: a group that is not the root has a cgroup.type.
	if err := unix.Fstatat(dir, cgroupTypeFile, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		//: the root of the machine, or of a namespace: a policy there binds everything.
		return "hierarchy-root", unix.EPERM
	}
	//: a group below a root.
	return "", nil
}

// add loads a program and attaches it to the group with BPF_F_ALLOW_MULTI,
// beside the programs already there. On a failure nothing of it remains.
func (h *handle) add(typ ebpf.ProgramType, attach ebpf.AttachType, insns asm.Instructions) error {
	prog, err := load(typ, attach, insns)
	//: refused before the group was touched.
	if err != nil {
		//: LOAD_REFUSED.
		return err
	}
	err = link.RawAttachProgram(link.RawAttachProgramOptions{
		Target:  h.dir,
		Program: prog,
		Attach:  attach,
		Flags:   unix.BPF_F_ALLOW_MULTI,
	})
	//: the group did not take it: nothing to detach, only a descriptor to close.
	if err != nil {
		//: ATTACH_FAILED.
		return withCleanup(attachFailed(err, h.path, "attach"), prog.Close())
	}
	h.parts = append(h.parts, attached{prog: prog, attach: attach})
	//: attached.
	return nil
}

// Close detaches every program, then releases them, the maps and the
// directory. A second Close is nil.
func (h *handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	//: closed already.
	if h.closed {
		//: nothing left.
		return nil
	}
	//: every failure, detach or close, typed once.
	if err := h.releaseLocked(); err != nil {
		//: PROGRAM_FAILED.
		return programFailed(err, "detach", errs.String("cgroup", h.path))
	}
	//: detached.
	return nil
}

// abandon undoes a failed attachment — detaches what was attached and
// releases everything — and returns cause, the undo's own failure in a
// field.
func (h *handle) abandon(cause error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	//: the cause, the cleanup beside it.
	return withCleanup(cause, h.releaseLocked())
}

// releaseLocked detaches the programs from the group, the last attached
// first, each by its own descriptor so BPF_F_ALLOW_MULTI keeps the others,
// then closes the programs, the maps and the directory. It returns every
// failure, joined.
func (h *handle) releaseLocked() error {
	var failures []error
	//: detach first: a closed descriptor cannot name its program.
	for _, part := range slices.Backward(h.parts) {
		failures = append(failures, link.RawDetachProgram(link.RawDetachProgramOptions{
			Target:  h.dir,
			Program: part.prog,
			Attach:  part.attach,
		}))
	}
	//: the programs.
	for _, part := range h.parts {
		failures = append(failures, part.prog.Close())
	}
	//: the maps.
	for _, m := range h.maps {
		failures = append(failures, m.Close())
	}
	failures = append(failures, unix.Close(h.dir))
	h.parts, h.maps, h.closed = nil, nil, true
	//: nil when everything went.
	return errors.Join(failures...)
}

// skbHandle is a cgroup_skb pair and the counters it reads.
type skbHandle struct {
	*handle
	counters *ebpf.Map
}

// makeMaps creates the two tries and the counters, fills the tries, and
// returns the maps by the names the instructions reference.
func (s *skbHandle) makeMaps(policy corebpf.SKBPolicyValue) (map[string]*ebpf.Map, error) {
	var v4, v6 []corebpf.PrefixRuleValue
	//: each rule in its family's trie.
	for _, r := range policy.Rules {
		//: an IPv4 prefix; Validate refused the IPv4-mapped ones.
		if r.Prefix.Addr().Is4() {
			v4 = append(v4, r)
			continue
		}
		v6 = append(v6, r)
	}
	lpm4, err := s.trie(mapLPM4, uint32(ip4Len), v4)
	//: the IPv4 trie.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, err
	}
	lpm6, err := s.trie(mapLPM6, uint32(ip6Len), v6)
	//: the IPv6 trie.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, err
	}
	s.counters, err = s.newMap(&ebpf.MapSpec{
		Name:       mapCounters,
		Type:       ebpf.Array,
		KeySize:    u32Size,
		ValueSize:  countersValue,
		MaxEntries: counterSlots,
	})
	//: the counters.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, err
	}
	//: the three maps, by name.
	return map[string]*ebpf.Map{mapLPM4: lpm4, mapLPM6: lpm6, mapCounters: s.counters}, nil
}

// trie creates one family's LPM trie, sized for its rules, and fills it.
func (s *skbHandle) trie(name string, addrLen uint32, rules []corebpf.PrefixRuleValue) (*ebpf.Map, error) {
	m, err := s.newMap(&ebpf.MapSpec{
		Name:       name,
		Type:       ebpf.LPMTrie,
		KeySize:    u32Size + addrLen,
		ValueSize:  lpmValueSize,
		MaxEntries: uint32(max(1, len(rules))),
		Flags:      unix.BPF_F_NO_PREALLOC,
	})
	//: refused.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, err
	}
	//: one entry per prefix.
	for _, r := range rules {
		//: a full or refused trie.
		if err := m.Put(prefixKey(r.Prefix), prefixValue(r.Verdict)); err != nil {
			//: LOAD_REFUSED.
			return nil, loadRefused(err, "map-update")
		}
	}
	//: filled.
	return m, nil
}

// newMap creates a map the handle releases.
func (s *skbHandle) newMap(spec *ebpf.MapSpec) (*ebpf.Map, error) {
	m, err := ebpf.NewMap(spec)
	//: the kernel's refusal.
	if err != nil {
		//: LOAD_REFUSED.
		return nil, loadRefused(err, "map")
	}
	s.maps = append(s.maps, m)
	//: created.
	return m, nil
}

// skbProgram compiles one hook of an IP policy and resolves its map
// references to the maps made for it.
func skbProgram(d direction, def corebpf.Verdict, maps map[string]*ebpf.Map) (asm.Instructions, error) {
	insns := skbInstructions(d, def)
	//: by the names the instructions reference.
	for name, m := range maps {
		//: a reference the program does not hold.
		if err := insns.AssociateMap(name, m); err != nil {
			//: LOAD_REFUSED.
			return nil, loadRefused(err, "associate")
		}
	}
	//: resolved.
	return insns, nil
}

// Counters reads both slots.
func (s *skbHandle) Counters() (corebpf.CountersValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: after Close the map is gone.
	if s.closed {
		//: PROGRAM_FAILED.
		return corebpf.CountersValue{}, errs.Wrap(corebpf.ProgramFailed, errs.WrapParams{}, errs.String("op", "counters"), errs.String("state", "closed"))
	}
	var in, out corebpf.TrafficValue
	//: ingress then egress.
	if err := errors.Join(s.counters.Lookup(uint32(ingress.slot), &in), s.counters.Lookup(uint32(egress.slot), &out)); err != nil {
		//: PROGRAM_FAILED.
		return corebpf.CountersValue{}, programFailed(err, "counters")
	}
	//: the two counts.
	return corebpf.CountersValue{Ingress: in, Egress: out}, nil
}

// unameRelease is the running kernel's release.
func unameRelease() (string, error) {
	var u unix.Utsname
	//: uname(2).
	if err := unix.Uname(&u); err != nil {
		//: unreadable.
		return "", err
	}
	//: the release, NUL-terminated in its array.
	return unix.ByteSliceToString(u.Release[:]), nil
}

// raiseMemlock lifts RLIMIT_MEMLOCK to infinity for this process.
func raiseMemlock() error {
	//: CAP_SYS_RESOURCE, before 5.11.
	return unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY})
}

// loadRefused is LOAD_REFUSED over the kernel's error, the step named.
func loadRefused(err error, step string) error {
	//: the cause in the chain, the step as a field.
	return errs.Wrap(err, corebpf.LoadRefused.Params(), errs.String("step", step))
}

// attachFailed is ATTACH_FAILED over the kernel's error, the group and the
// step named.
func attachFailed(err error, cgroup, step string) error {
	//: the cause in the chain, the group and the step as fields.
	return errs.Wrap(err, corebpf.AttachFailed.Params(), errs.String("cgroup", cgroup), errs.String("step", step))
}

// programFailed is PROGRAM_FAILED over the kernel's error, the operation
// named.
func programFailed(err error, op string, fields ...errs.FieldValue) error {
	//: the cause in the chain, the operation and the caller's fields.
	return errs.Wrap(err, corebpf.ProgramFailed.Params(), append([]errs.FieldValue{errs.String("op", op)}, fields...)...)
}

// withCleanup returns cause, with the failure of the cleanup that followed
// it — when there was one — as a field: the cause keeps its code.
func withCleanup(cause, cleanup error) error {
	//: a clean undo changes nothing.
	if cleanup == nil {
		//: the cause as it is.
		return cause
	}
	//: origin wins: the cause's code, the cleanup's text beside it.
	return errs.Wrap(cause, errs.WrapParams{}, errs.String("cleanup", cleanup.Error()))
}
