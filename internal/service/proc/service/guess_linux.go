//go:build linux

package service

import (
	"bytes"
	"os"
	"strconv"
)

// parentOf reads a process's parent from /proc/<pid>/stat: the field after
// the state, which follows the last ")" — the command's name may hold any.
func parentOf(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	//: gone, or not readable.
	if err != nil {
		//: unknown.
		return 0, false
	}
	_, rest, named := bytes.CutLast(raw, []byte(")"))
	//: no command name.
	if !named {
		//: unknown.
		return 0, false
	}
	fields := bytes.Fields(rest)
	//: the state, then the parent.
	if len(fields) < 2 {
		//: unknown.
		return 0, false
	}
	parent, err := strconv.Atoi(string(fields[1]))
	//: a parent, or not.
	return parent, err == nil
}
