package entitlement_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// enrolSubject is a canonical v4 UUID the enrolment tests mint under.
const enrolSubject string = "11111111-2222-4333-8444-555555555555"

// uuidShape matches the canonical v4 form the key filename must carry.
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewSubjectID pins the identifier shape, because entitlement.DiscoverSubject finds an
// enrolment by matching the filename against that exact pattern.
func TestNewSubjectID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		runs int
	}{
		{name: "produces a well-formed v4", runs: 1},
		{name: "successive draws differ", runs: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seen := map[string]bool{}
			for range tt.runs {
				got := entitlement.NewSubjectID()
				//: The version nibble and the variant bits are asserted by
				//: uuidShape itself, which is what entitlement.DiscoverSubject matches
				//: filenames against; stdlib uuid must keep satisfying it.
				if !uuidShape.MatchString(got) {
					t.Errorf("entitlement.NewSubjectID() = %q, want canonical v4 form", got)
				}
				//: Collisions would silently merge two clients' identities.
				if seen[got] {
					t.Errorf("entitlement.NewSubjectID() repeated %q", got)
				}
				seen[got] = true
			}
		})
	}
}

// TestGenerateKeyPair pins the on-disk result, including the permission that
// the possession proof depends on.
func TestGenerateKeyPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		uuid string
	}{
		{name: "writes both halves under the uuid", uuid: sampleUUID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			authorized, err := entitlement.GenerateKeyPair(&testProduct, dir, tt.uuid)
			//: A generation failure would leave the user unenrollable.
			if err != nil {
				t.Fatalf("entitlement.GenerateKeyPair(&testProduct, ) error = %v, want nil", err)
			}
			if !strings.HasPrefix(authorized, "ssh-ed25519 ") {
				t.Errorf("public key = %q, want an ssh-ed25519 authorized-keys line", authorized)
			}

			info, statErr := os.Stat(entitlement.PrivateKeyPath(dir, tt.uuid))
			if statErr != nil {
				t.Fatalf("stat private key: %v", statErr)
			}
			//: The key must exist as a regular file. Its MODE is asserted in
			//: enroll_unix_external_test.go: os.Stat reports 0666 on Windows
			//: whatever os.WriteFile was asked for.
			if !info.Mode().IsRegular() {
				t.Errorf("private key is not a regular file: %v", info.Mode())
			}

			//: The pair must be immediately usable end to end.
			if _, loadErr := entitlement.SignerFromFile(dir, tt.uuid); loadErr != nil {
				t.Errorf("entitlement.SignerFromFile() after generation: %v", loadErr)
			}
			discovered, discErr := entitlement.DiscoverSubject(dir)
			if discErr != nil {
				t.Fatalf("entitlement.DiscoverSubject() after generation: %v", discErr)
			}
			if discovered != tt.uuid {
				t.Errorf("entitlement.DiscoverSubject() = %q, want %q", discovered, tt.uuid)
			}
		})
	}
}

// TestIssueURL pins that the request carries what the workflow needs and
// nothing the user would have to hold a credential for.
func TestIssueURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rotation  bool
		wantLabel string
	}{
		{name: "first enrolment is labelled request", rotation: false, wantLabel: "license%3Arequest"},
		{name: "rotation is labelled update", rotation: true, wantLabel: "license%3Aupdate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := entitlement.IssueURL(&testProduct, sampleUUID, "ssh-ed25519 AAAA test\n", tt.rotation)
			//: The label is what routes the issue to the right workflow.
			if !strings.Contains(got, tt.wantLabel) {
				t.Errorf("entitlement.IssueURL(&testProduct, ) = %q, want it to carry label %q", got, tt.wantLabel)
			}
			//: The subject must travel or the vendor cannot act on it.
			if !strings.Contains(got, sampleUUID) {
				t.Errorf("entitlement.IssueURL(&testProduct, ) = %q, want it to carry the subject", got)
			}
			//: A token in the link would defeat the point of the design.
			if strings.Contains(strings.ToLower(got), "token") {
				t.Errorf("entitlement.IssueURL(&testProduct, ) = %q, want no credential in the link", got)
			}
		})
	}
}

// TestGenerateKeyPairCreatesDirectory pins that enrolment works on a machine
// that has never used ssh. A fresh container or CI runner has no ~/.ssh, and
// failing there would make enrolment impossible in exactly the environments
// that need it most — which is how this was found, from a red CI job.
func TestGenerateKeyPairCreatesDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		preexist bool
		mode     os.FileMode
	}{
		{name: "an absent directory is created", preexist: false},
		{name: "an existing directory is left alone", preexist: true, mode: 0o700},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := t.TempDir()
			sshDir := filepath.Join(base, ".ssh")
			//: Only the pre-existing cases start with a directory.
			if tt.preexist {
				if err := os.MkdirAll(sshDir, 0o700); err != nil {
					t.Fatalf("creating fixture dir: %v", err)
				}
				//: Restore write permission before the temp dir is removed:
				//: t.TempDir's own cleanup cannot delete a 0o500 directory's
				//: contents, and it reports that as a test failure rather
				//: than the fixture detail it is. A failure here is worth
				//: surfacing — a silently un-restored mode would make the
				//: real cleanup error unexplainable.
				t.Cleanup(func() {
					if chmodErr := os.Chmod(sshDir, 0o700); chmodErr != nil {
						t.Errorf("restoring fixture mode: %v", chmodErr)
					}
				})
				if err := os.Chmod(sshDir, tt.mode); err != nil {
					t.Fatalf("setting fixture mode: %v", err)
				}
			}

			_, err := entitlement.GenerateKeyPair(&testProduct, sshDir, sampleUUID)
			if err != nil {
				t.Fatalf("entitlement.GenerateKeyPair(&testProduct, ) error = %v, want nil", err)
			}

			info, statErr := os.Stat(sshDir)
			if statErr != nil {
				t.Fatalf("stat key directory: %v", statErr)
			}
			//: The directory must exist and be a directory. Its MODE is
			//: asserted in enroll_unix_external_test.go, since Windows
			//: reports 0777 for every directory whatever its ACL.
			if !info.IsDir() {
				t.Errorf("key directory %s is not a directory", sshDir)
			}
		})
	}
}

// TestGenerateKeyPairFailuresAreNameable pins that every step of enrolment
// after the subject check reports under one sentinel a caller can match.
//
// Each of these was a bare fmt.Errorf with no sentinel and no code, so
// errs.CodeOf answered (0.0.0.0, false) and errors.Is matched nothing: a CLI
// distinguishing "you are not enrolled" from "enrolling just failed" — which
// are two different instructions to the same operator — had nothing to branch
// on. ErrNoLicence would have been the wrong answer for the second, since its
// remedy is to run the command that has already failed.
func TestGenerateKeyPairFailuresAreNameable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// arrange returns the key directory, after putting something in the
		// way of the step under test.
		arrange func(t *testing.T) string
		reason  string
	}{
		{
			name: "the key directory cannot be created",
			arrange: func(t *testing.T) string {
				t.Helper()
				//: A regular file where a directory component must be: every
				//: platform refuses to create a child of it.
				blocked := filepath.Join(t.TempDir(), "not-a-dir")
				if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
					t.Fatalf("writing blocker: %v", err)
				}
				return filepath.Join(blocked, ".ssh")
			},
			reason: "a fresh container has no key directory, so MkdirAll is the first step that can fail",
		},
		{
			name: "the private half cannot be written",
			arrange: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				//: A directory wearing the private key's name: the open
				//: succeeds at neither truncating nor creating.
				if err := os.Mkdir(entitlement.PrivateKeyPath(dir, enrolSubject), 0o700); err != nil {
					t.Fatalf("creating blocker: %v", err)
				}
				return dir
			},
			reason: "the private half is written first, so its failure is the one that leaves nothing behind",
		},
		{
			name: "the published half cannot be written",
			arrange: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				//: Same blocker, one step later: the private half lands and
				//: the published one does not.
				if err := os.Mkdir(entitlement.PublicKeyPath(dir, enrolSubject), 0o700); err != nil {
					t.Fatalf("creating blocker: %v", err)
				}
				return dir
			},
			reason: "a pair with no published half is what the next DiscoverSubject has to read as unusable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := entitlement.GenerateKeyPair(nil, tt.arrange(t), enrolSubject)
			if err == nil {
				t.Fatalf("GenerateKeyPair() error = nil, want a failure (%s)", tt.reason)
			}
			if !errors.Is(err, entitlement.EnrolmentFailed) {
				t.Errorf("GenerateKeyPair() error = %v, want it to carry EnrolmentFailed (%s)", err, tt.reason)
			}
			//: The contract's "not enrolled" sentinel must NOT answer here.
			//: Its remedy is to enrol, which is the command that just failed.
			if errors.Is(err, coreent.ErrNoLicense) {
				t.Errorf("GenerateKeyPair() error = %v, want it NOT to read as ErrNoLicense (%s)", err, tt.reason)
			}
		})
	}
}
