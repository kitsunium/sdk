package streamaead

import (
	"bytes"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// readMode selects which decision branch of streamReader.Read a case drives.
const (
	// readHappy drives the full happy path: header + chunk + buffered-serve + EOF.
	readHappy int = iota
	// readBadHeader fails on the first Read with a short header.
	readBadHeader
	// readTampered fails after the header on a corrupted chunk.
	readTampered
	// readBuffered drives the buffered-serve (len(plain)>0) branch with a tiny p.
	readBuffered
)

func rKey(t *testing.T) corecrypto.Key {
	t.Helper()
	//: deterministic 32-byte key.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{0x2}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// sealForReader produces a valid stream of pt under the deterministic-salt seam.
func sealForReader(t *testing.T, key corecrypto.Key, pt []byte) []byte {
	t.Helper()
	var dst bytes.Buffer
	var salt [saltLen]byte
	w, err := newWriterWithSalt(key, &dst, nil, salt)
	if err != nil {
		t.Fatalf("newWriterWithSalt: %v", err)
	}
	if _, werr := w.Write(pt); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	return dst.Bytes()
}

// sealChunkFor seals pt under key + the deterministic-salt-derived stream key at
// counter 0 with flag, mirroring what the reader expects for its first chunk.
func sealChunkFor(t *testing.T, key corecrypto.Key, pt []byte, flag byte) []byte {
	t.Helper()
	var salt [saltLen]byte
	gcm, err := newGCM(key.Bytes(), salt[:])
	if err != nil {
		t.Fatalf("newGCM: %v", err)
	}
	nonce := chunkNonce(0, flag)
	//: aad is nil to match the reader constructed with nil aad above.
	return gcm.Seal(nil, nonce[:], pt, nil)
}

func Test_newReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"constructs a lazily-initialised reader"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: construction never fails and never reads.
			if r := newReader(rKey(t), bytes.NewReader(nil), nil); r == nil {
				t.Errorf("newReader returned nil")
			}
		})
	}
}

func Test_streamReader_Read(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    int
		wantErr error
	}{
		{"happy path decrypts then EOFs", readHappy, nil},
		{"short header fails", readBadHeader, corecrypto.StreamTruncated},
		{"tampered chunk fails", readTampered, corecrypto.DecryptionFailed},
		{"buffered-serve returns remaining bytes", readBuffered, nil},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runReadCase(t, c.mode, c.wantErr)
		})
	}
}

// runReadCase drives one decision branch of streamReader.Read directly via the
// Read method (not io.ReadAll), so the linter sees each outcome exercised.
func runReadCase(t *testing.T, mode int, wantErr error) {
	t.Helper()
	key := rKey(t)
	//: a bad-header case feeds a short wire so the first Read fails on the header.
	if mode == readBadHeader {
		r := newReader(key, bytes.NewReader([]byte{0x02}), nil)
		//: the header branch returns the typed error and no bytes.
		if n, err := r.Read(make([]byte, 16)); n != 0 || err != wantErr {
			t.Fatalf("Read=(%d,%v) want (0,%v)", n, err, wantErr)
		}
		return
	}
	pt := []byte("decision coverage payload")
	wire := sealForReader(t, key, pt)
	//: a tampered case flips a ciphertext byte so the chunk branch fails.
	if mode == readTampered {
		wire[headerLen] ^= 0xff
		r := newReader(key, bytes.NewReader(wire), nil)
		//: the chunk-error branch surfaces the typed sentinel.
		if _, err := r.Read(make([]byte, 16)); err != wantErr {
			t.Fatalf("Read err=%v want %v", err, wantErr)
		}
		return
	}
	runReadHappy(t, key, wire, pt, mode)
}

// runReadHappy drives the success branches: the full-drain happy path and the
// small-buffer buffered-serve branch, then asserts clean EOF.
func runReadHappy(t *testing.T, key corecrypto.Key, wire, pt []byte, mode int) {
	t.Helper()
	r := newReader(key, bytes.NewReader(wire), nil)
	//: a tiny p forces the buffered-serve branch on the second Read.
	step := len(pt)
	if mode == readBuffered {
		step = 1
	}
	var got []byte
	//: drive Read in steps until EOF; each call decrypts or serves the buffer.
	for {
		buf := make([]byte, step)
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		//: clean EOF only ever follows the final-flag chunk.
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	//: every byte must round-trip regardless of the read step size.
	if !bytes.Equal(got, pt) {
		t.Errorf("Read got %q want %q", got, pt)
	}
}

func Test_streamReader_readHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wire    []byte
		wantErr error
	}{
		{"short header is truncated", []byte{0x02}, corecrypto.StreamTruncated},
		{"empty input is truncated", nil, corecrypto.StreamTruncated},
		{"box lead byte is rejected", append([]byte{0x01, 0x01}, bytes.Repeat([]byte{0}, saltLen)...), corecrypto.DecryptionFailed},
		{"bad algID is rejected", append([]byte{0x02, 0x09}, bytes.Repeat([]byte{0}, saltLen)...), corecrypto.DecryptionFailed},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReader(rKey(t), bytes.NewReader(c.wire), nil)
			//: readHeader classifies short vs wrong-format inputs.
			if err := r.readHeader(); err != c.wantErr {
				t.Errorf("readHeader err=%v want %v", err, c.wantErr)
			}
		})
	}
}

func Test_streamReader_nextChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pt      []byte
		corrupt bool
		wantErr error
	}{
		{"verified chunk decrypts", []byte("abc"), false, nil},
		{"empty final chunk decrypts", []byte{}, false, nil},
		{"tampered chunk fails", []byte("abc"), true, corecrypto.DecryptionFailed},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := rKey(t)
			wire := sealForReader(t, key, c.pt)
			//: corrupt a ciphertext byte to drive the open-failure branch.
			if c.corrupt {
				wire[headerLen] ^= 0xff
			}
			r := newReader(key, bytes.NewReader(wire), nil)
			if err := r.readHeader(); err != nil {
				t.Fatalf("readHeader: %v", err)
			}
			//: nextChunk surfaces the verified plaintext or the typed error.
			if err := r.nextChunk(); err != c.wantErr {
				t.Errorf("nextChunk err=%v want %v", err, c.wantErr)
			}
		})
	}
}

func Test_streamReader_openChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		flag     byte
		atEOF    bool
		tamper   bool
		wantErr  error
		wantDone bool
	}{
		{"genuine final chunk opens and marks done", flagFinal, true, false, nil, true},
		{"genuine non-final chunk opens and continues", flagMore, false, false, nil, false},
		{"tampered chunk fails authentication", flagFinal, true, true, corecrypto.DecryptionFailed, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := rKey(t)
			//: prime a reader so r.gcm is derived from the deterministic-salt header.
			r := newReader(key, bytes.NewReader(sealForReader(t, key, []byte("seed"))), nil)
			if err := r.readHeader(); err != nil {
				t.Fatalf("readHeader: %v", err)
			}
			//: seal a fresh chunk under the reader's derived stream key.
			wire := sealChunkFor(t, key, []byte("chunk"), c.flag)
			//: the tamper case flips a ciphertext byte so GCM authentication fails.
			if c.tamper {
				wire[0] ^= 0xff
			}
			//: open the chunk directly across its decision branches.
			err := r.openChunk(wire, c.flag, c.atEOF)
			//: the verdict and the done bit must match the branch expectation.
			if err != c.wantErr || (r.state&stateDone != 0) != c.wantDone {
				t.Errorf("openChunk err=%v done=%v want err=%v done=%v", err, r.state&stateDone != 0, c.wantErr, c.wantDone)
			}
		})
	}
}

func Test_streamReader_readWire(t *testing.T) {
	t.Parallel()
	const chunk int = chunkSize
	tests := []struct {
		name    string
		pt      []byte
		wantEOF bool
	}{
		{"single short chunk is final", []byte("q"), true},
		{"a full first chunk is non-final", bytes.Repeat([]byte{0x1}, chunk), false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := rKey(t)
			r := newReader(key, bytes.NewReader(sealForReader(t, key, c.pt)), nil)
			if err := r.readHeader(); err != nil {
				t.Fatalf("readHeader: %v", err)
			}
			//: readWire reports the valid byte count and whether the chunk is final.
			n, atEOF, err := r.readWire()
			if err != nil || atEOF != c.wantEOF || n < gcmTagLen {
				t.Errorf("readWire=(%d bytes,%v,%v) wantEOF=%v", n, atEOF, err, c.wantEOF)
			}
		})
	}
}

func Test_streamReader_fillWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     string
		wantEOF bool
		wantErr error
	}{
		{"short source is the final chunk", "short", true, nil},
		{"empty source is the final chunk", "", true, nil},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReader(rKey(t), bytes.NewReader([]byte(c.src)), nil)
			//: a source shorter than a full chunk ends as the final chunk; fill
			//: reads in place into r.wire from offset 0.
			n, atEOF, err := r.fillWire(0)
			if err != c.wantErr || atEOF != c.wantEOF || string(r.wire[:n]) != c.src {
				t.Errorf("fillWire=(%q,%v,%v) want (%q,%v,%v)", r.wire[:n], atEOF, err, c.src, c.wantEOF, c.wantErr)
			}
		})
	}
}

func Test_streamReader_peekNext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remaining string
		wantEOF   bool
	}{
		{"another byte means a non-final chunk", "x", false},
		{"no further byte means the final chunk", "", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newReader(rKey(t), bytes.NewReader([]byte(c.remaining)), nil)
			//: peekNext distinguishes a following chunk from EOF; the byte count it
			//: carries through is opaque to this branch.
			_, atEOF, err := r.peekNext(4)
			if err != nil || atEOF != c.wantEOF {
				t.Errorf("peekNext atEOF=%v want %v (err=%v)", atEOF, c.wantEOF, err)
			}
		})
	}
}
