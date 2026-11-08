//go:build linux && (amd64 || arm64)

package exec

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"unsafe"
)

// fullPayload is a payload with every field set.
func fullPayload() *sandboxPayload {
	return &sandboxPayload{
		cgroupNS: true,
		mounts: []sandboxMount{
			{kind: 1, readOnly: true, source: "/srv/a", target: "/mnt/a"},
			{kind: 2, target: "/tmp"},
		},
		root:       "/root",
		dir:        "/work",
		identity:   &sandboxIdentity{uid: 1000, gid: 100, setGroups: true, groups: []uint32{4, 27}},
		caps:       &sandboxCaps{bounding: 1 << 10, ambient: 1 << 10, secureBits: 0x2f, lastCap: 40},
		noNewPrivs: true,
		handled:    0x1fff,
		rules:      []sandboxRule{{access: 2, path: "/var/lib/x"}},
		filter:     []sockFilter{{code: bpfLdWAbs, k: 4}, {code: bpfRetK, k: seccompRetAllow}},
		pdeathsig:  15,
		parentPID:  42,
		failStep:   3,
		traceFD:    3,
	}
}

// Test_sandboxPayloadRoundTrip pins that what the parent encodes is what the
// child decodes, field for field.
func Test_sandboxPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]*sandboxPayload{
		"empty": {traceFD: sandboxNoTrace, mounts: []sandboxMount{}, rules: []sandboxRule{}, filter: []sockFilter{}},
		"full":  fullPayload(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, errno := decodeSandbox(encodeSandbox(p))
			if errno != 0 {
				t.Fatalf("decodeSandbox(encodeSandbox) = %v", errno)
			}
			if !reflect.DeepEqual(got, p) {
				t.Fatalf("round trip:\n got  %+v\n want %+v", got, p)
			}
		})
	}
}

// Test_decodeSandboxRefuses pins #728's strict payload: an unknown version, a
// length that is not the data's, a byte left over, a field cut short, a count
// or string past its bound, a NUL in a path, a boolean that is none — each is
// refused, never half-read.
func Test_decodeSandboxRefuses(t *testing.T) {
	t.Parallel()
	good := encodeSandbox(fullPayload())
	mutate := func(f func([]byte) []byte) []byte {
		return f(append([]byte(nil), good...))
	}
	relen := func(b []byte) []byte {
		binary.LittleEndian.PutUint32(b[8:], uint32(len(b)))
		return b
	}
	cases := map[string][]byte{
		"too short":        good[:sandboxHeaderLen-1],
		"wrong magic":      mutate(func(b []byte) []byte { b[0] = 'X'; return b }),
		"unknown version":  mutate(func(b []byte) []byte { binary.LittleEndian.PutUint16(b[4:], sandboxVersion+1); return b }),
		"reserved set":     mutate(func(b []byte) []byte { b[6] = 1; return b }),
		"length too large": mutate(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], uint32(len(b)+1)); return b }),
		"length too small": mutate(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:], uint32(len(b)-1)); return b }),
		"trailing byte":    mutate(func(b []byte) []byte { return relen(append(b, 0)) }),
		"cut short":        mutate(func(b []byte) []byte { return relen(b[:len(b)-1]) }),
		"mount count past bound": mutate(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[sandboxHeaderLen+1:], uint32(sandboxMaxItems+1))
			return b
		}),
		"string past bound": mutate(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[sandboxHeaderLen+1+4+2:], uint32(sandboxMaxString+1))
			return b
		}),
		"NUL in a path": mutate(func(b []byte) []byte { b[sandboxHeaderLen+1+4+2+4] = 0; return b }),
		"flag not boolean": mutate(func(b []byte) []byte {
			b[sandboxHeaderLen+1+4+1] = 2
			return b
		}),
		"cgroup flag not boolean": mutate(func(b []byte) []byte {
			b[sandboxHeaderLen] = 2
			return b
		}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if p, errno := decodeSandbox(data); errno != syscall.EINVAL {
				t.Fatalf("decodeSandbox = (%+v, %v), want EINVAL", p, errno)
			}
		})
	}
}

// memfd creates an unsealed, sealable memfd holding data.
func memfd(t *testing.T, data []byte) int {
	t.Helper()
	name, _ := syscall.BytePtrFromString("test")
	r, _, errno := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(name)), mfdCloexec|mfdAllowSealing, 0)
	if errno != 0 {
		t.Fatalf("memfd_create: %v", errno)
	}
	if e := writePayload(int(r), data); e != 0 {
		t.Fatalf("write: %v", e)
	}
	return int(r)
}

// seal adds seals to fd.
func seal(t *testing.T, fd int, seals uintptr) {
	t.Helper()
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), fAddSeals, seals); errno != 0 {
		t.Fatalf("F_ADD_SEALS: %v", errno)
	}
}

// Test_readSealedPayload pins the child's side of #728: the payload is read
// only from a memfd carrying every seal — write, grow, shrink and seal — whose
// size is the length its header states; an unsealed memfd, a partly sealed
// one, a plain file, and a header lying about the size are refused.
func Test_readSealedPayload(t *testing.T) {
	t.Parallel()
	data := encodeSandbox(fullPayload())
	t.Run("sealed", func(t *testing.T) {
		t.Parallel()
		fd, errno := sealedPayload(data)
		if errno != 0 {
			t.Fatalf("sealedPayload: %v", errno)
		}
		p, errno := readSealedPayload(fd)
		if errno != 0 || !reflect.DeepEqual(p, fullPayload()) {
			t.Fatalf("readSealedPayload = (%+v, %v), want the payload", p, errno)
		}
	})
	refused := map[string]func(t *testing.T) int{
		"no seal": func(t *testing.T) int { return memfd(t, data) },
		"sealed against writes only": func(t *testing.T) int {
			fd := memfd(t, data)
			seal(t, fd, fSealWrite)
			return fd
		},
		"every seal but the seal seal": func(t *testing.T) int {
			fd := memfd(t, data)
			seal(t, fd, fSealWrite|fSealGrow|fSealShrink)
			return fd
		},
		"a plain file": func(t *testing.T) int {
			path := filepath.Join(t.TempDir(), "payload")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			return fd
		},
		"a header lying about the size": func(t *testing.T) int {
			bad := append([]byte(nil), data...)
			binary.LittleEndian.PutUint32(bad[8:], uint32(len(bad)+8))
			fd, errno := sealedPayload(bad)
			if errno != 0 {
				t.Fatalf("sealedPayload: %v", errno)
			}
			return fd
		},
		"an unknown version, sealed": func(t *testing.T) int {
			bad := append([]byte(nil), data...)
			binary.LittleEndian.PutUint16(bad[4:], sandboxVersion+1)
			fd, errno := sealedPayload(bad)
			if errno != 0 {
				t.Fatalf("sealedPayload: %v", errno)
			}
			return fd
		},
	}
	for name, open := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if p, errno := readSealedPayload(open(t)); errno == 0 {
				t.Fatalf("readSealedPayload = %+v, want a refusal", p)
			}
		})
	}
}
