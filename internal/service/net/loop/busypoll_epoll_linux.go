//go:build linux

package loop

import (
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The two requests of struct epoll_params, _IOW and _IOR of type 0x8A on its
// 8 bytes, as this architecture encodes a direction.
const (
	epiocSParams uintptr = iocWrite<<iocDirShift | 8<<16 | 0x8A<<8 | 1
	epiocGParams uintptr = iocRead<<iocDirShift | 8<<16 | 0x8A<<8 | 2
)

// epollParams is struct epoll_params (include/uapi/linux/eventpoll.h, 6.9).
type epollParams struct {
	usecs  uint32
	budget uint16
	prefer uint8
	pad    uint8
}

// epollBusy is busy-poll on one epoll loop: the parameters its instance
// takes, none when the row is off, and the kernel's refusal of them.
type epollBusy struct {
	on     bool
	params epollParams
	hook   func(string)
	errno  syscall.Errno
}

// setEpollParams is EPIOCSPARAMS on epfd.
func setEpollParams(epfd int, p *epollParams) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(epfd), epiocSParams, uintptr(unsafe.Pointer(p)))
	//: zero once taken.
	return errno
}

// probeBusyPollEpoll is busy-poll's stage on epoll: EPIOCSPARAMS with a zero
// window on a throwaway instance (ENOTTY before 6.9). It reports the row and
// returns what each loop's instance takes; a switched-off row issues nothing.
func probeBusyPollEpoll(cfg *Config, sw capSwitches, rep *capReport, sz *sizing) epollBusy {
	busy := epollBusy{hook: cfg.hooks.busyPoll}
	//: the operator's switch: never probed.
	if sw.disabled(corenet.CapabilityBusyPoll) {
		//: off.
		return busy
	}
	errno := epollProbe(&cfg.hooks)
	//: the kernel refused the stage.
	if errno != 0 {
		rep.found(sw, corenet.CapabilityBusyPoll, false, "epiocsparams: "+errno.Error())
		//: off.
		return busy
	}
	//: present, not asked for.
	if !sw.wants(corenet.CapabilityBusyPoll) {
		rep.found(sw, corenet.CapabilityBusyPoll, true, "")
		//: off.
		return busy
	}
	busy.on = true
	busy.params = epollParams{usecs: sz.busyPoll.usecs, budget: sz.busyPoll.budget, prefer: 1}
	rep.set(corenet.CapabilityBusyPoll, statusOn, reasonBusyPollOn)
	//: each loop's instance takes it.
	return busy
}

// epollProbe asks a throwaway instance for EPIOCSPARAMS; a test plays a
// kernel before 6.9.
func epollProbe(hooks *loopHooks) syscall.Errno {
	//: a test plays a kernel before 6.9.
	if hooks.failStage == "busy_poll" {
		//: as it would answer.
		return syscall.ENOTTY
	}
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	//: no instance to ask.
	if err != nil {
		//: the kernel's answer.
		return syscall.Errno(errnoOf(err))
	}
	defer closeFDs([]int{epfd})
	callHook(hooks.busyPoll, "epiocsparams")
	//: zero once taken.
	return setEpollParams(epfd, new(epollParams))
}

// apply sets the loop's instance's parameters, when the row is on, and keeps
// the kernel's refusal for the report.
func (b *epollBusy) apply(epfd int) {
	//: off.
	if !b.on {
		//: nothing asked.
		return
	}
	callHook(b.hook, "epiocsparams")
	b.errno = setEpollParams(epfd, &b.params)
}

// reportBusyEpoll turns the row missing when a loop's instance refused what
// the probe's took.
func reportBusyEpoll(rep *capReport, loops []*epollLoop) {
	//: the first refusal is the one reported.
	for _, el := range loops {
		//: refused.
		if el.busy.errno != 0 {
			rep.set(corenet.CapabilityBusyPoll, statusMissing, "epiocsparams: "+el.busy.errno.Error())
			//: reported.
			return
		}
	}
}
