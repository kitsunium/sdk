package self

import "testing"

// TestContainerOf pins #938's container id over the cgroup shapes the
// runtimes write: docker, containerd's CRI plugin, CRI-O, podman, a bare id
// under kubepods, and paths that name no container.
func TestContainerOf(t *testing.T) {
	t.Parallel()
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := map[string]string{
		"/system.slice/docker-" + id + ".scope":                                    id,
		"/kubepods.slice/kubepods-burstable.slice/cri-containerd-" + id + ".scope": id,
		"/kubepods/burstable/pod1234/" + id:                                        id,
		"/machine.slice/libpod-" + id + ".scope/container":                         id,
		"/crio-" + id + ".scope":                                                   id,
		"/user.slice/user-1000.slice/session-3.scope":                              "",
		"/":                  "",
		"":                   "",
		"/docker/" + id[:63]: "",
	}
	for cgroupPath, want := range tests {
		if got := containerOf(cgroupPath); got != want {
			t.Errorf("containerOf(%q) = %q, want %q", cgroupPath, got, want)
		}
	}
}
