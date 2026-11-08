package self

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The procfs facts the kernel keeps about the process and the boot.
const (
	// selfStatFile's field 22 is the start time, in clock ticks since boot.
	selfStatFile string = "/proc/self/stat"
	// systemStatFile's btime line is the boot time, in seconds since the epoch.
	systemStatFile string = "/proc/stat"
	// pidNamespaceFile is the process's pid namespace, its inode the namespace id.
	pidNamespaceFile string = "/proc/self/ns/pid"
	// userHZ is the clock tick of /proc's times: USER_HZ, 100 on every Linux ABI.
	userHZ int64 = 100
	// startTimeIndex is field 22's index after the ")" ending field 2.
	startTimeIndex int = 22 - 3
	// btimeKey opens /proc/stat's boot-time line.
	btimeKey string = "btime"
	// nsPidKey opens /proc/self/status's per-namespace pid line.
	nsPidKey string = "NSpid:"
	// namespaceInit is a pid namespace's first process.
	namespaceInit string = "1"
)

// kernelStart reads when the kernel started the process: the boot time plus
// the start tick.
func kernelStart() (time.Time, bool) {
	stat, err := os.ReadFile(selfStatFile)
	boot, berr := os.ReadFile(systemStatFile)
	//: no procfs.
	if err != nil || berr != nil {
		return time.Time{}, false
	}
	line := string(stat)
	//: the command name may hold spaces and parentheses; the fields follow its last ")".
	end := strings.LastIndexByte(line, ')')
	fields := strings.Fields(line[end+1:])
	//: a stat line shorter than the kernel writes.
	if end < 0 || len(fields) <= startTimeIndex {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	bootSeconds, ok := bootTime(string(boot))
	//: unreadable.
	if err != nil || !ok {
		return time.Time{}, false
	}
	at := time.Unix(bootSeconds, 0).Add(time.Duration(ticks) * time.Second / time.Duration(userHZ))
	//: the kernel's word, to the tick.
	return at, true
}

// bootTime reads /proc/stat's btime.
func bootTime(stat string) (int64, bool) {
	//: one line per counter.
	for line := range strings.Lines(stat) {
		fields := strings.Fields(line)
		//: the boot time.
		if len(fields) == 2 && fields[0] == btimeKey {
			seconds, err := strconv.ParseInt(fields[1], 10, 64)
			return seconds, err == nil
		}
	}
	return 0, false
}

// pidNamespace reads the pid namespace's inode and whether the process is its
// pid 1 (the last NSpid entry: the innermost namespace's view).
func pidNamespace() (inode uint64, nsPid1 bool) {
	//: the namespace's identity.
	if info, err := os.Stat(pidNamespaceFile); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			inode = st.Ino
		}
	}
	status, err := os.ReadFile(statusFile)
	//: no status: the inode alone.
	if err != nil {
		return inode, false
	}
	//: the NSpid line.
	for line := range strings.Lines(string(status)) {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == nsPidKey {
			return inode, fields[len(fields)-1] == namespaceInit
		}
	}
	return inode, false
}
