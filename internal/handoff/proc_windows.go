package handoff

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformProcesses walks a Toolhelp snapshot for the pid and parent of every
// process, and asks each one for its command line. A process that will not
// answer — another user's, a protected one — stays in the table with an empty
// command line: it matches no pattern, but it still links its children to
// their ancestors.
func platformProcesses(ctx context.Context) ([]procInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("handoff: process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []procInfo
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		// pid 0 is the idle process, which has neither a command line nor
		// children worth following.
		if entry.ProcessID == 0 {
			continue
		}
		cmdline, cerr := commandLine(entry.ProcessID)
		if cerr != nil {
			cmdline = ""
		}
		out = append(out, procInfo{pid: int(entry.ProcessID), ppid: int(entry.ParentProcessID), cmdline: cmdline})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("handoff: walk the process snapshot: %w", err)
	}
	return out, nil
}

// commandLine asks the kernel for a process's command line, which only needs
// the limited query right (Windows 8.1 and later).
func commandLine(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	// The answer is a UNICODE_STRING followed by the text it points at. A
	// buffer that is too small is told the size it needs.
	size := uint32(1024)
	for range 4 {
		buf := make([]byte, size)
		var need uint32
		err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
			unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			return (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String(), nil
		}
		if need <= size {
			return "", err
		}
		size = need
	}
	return "", fmt.Errorf("handoff: command line of %d kept growing", pid)
}

// unicodeString is the layout of a UNICODE_STRING as it sits in another
// process: the buffer is an address over there, so it is kept as a number
// rather than as a Go pointer.
type unicodeString struct {
	length    uint16
	maxLength uint16
	buffer    uintptr
}

// processBasicInformation mirrors PROCESS_BASIC_INFORMATION with the PEB
// address as a number, for the same reason.
type processBasicInformation struct {
	exitStatus   int32
	peb          uintptr
	affinityMask uintptr
	basePriority int32
	pid          uintptr
	ppid         uintptr
}

// Where the two fields on the way to the working directory sit: the process
// parameters in the PEB, and the current directory in those parameters. The
// offsets come from the x/sys layouts, which match the native ones.
var (
	pebParamsOffset = unsafe.Offsetof(windows.PEB{}.ProcessParameters)
	paramsCwdOffset = unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.CurrentDirectory)
)

// platformProcessCwd reads a process's working directory out of its memory:
// Windows has no call that answers it for another process. The path goes PEB →
// process parameters → CurrentDirectory.DosPath.
//
// A process of the other bitness (a 32-bit one under WOW64 next to a 64-bit
// Ferry) keeps its parameters where this walk does not look, so it is reported
// as unreadable and skipped, like any other process that will not answer.
func platformProcessCwd(ctx context.Context, pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return "", fmt.Errorf("handoff: open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)

	var self, other bool
	if err := windows.IsWow64Process(windows.CurrentProcess(), &self); err != nil {
		return "", fmt.Errorf("handoff: bitness of this process: %w", err)
	}
	if err := windows.IsWow64Process(h, &other); err != nil {
		return "", fmt.Errorf("handoff: bitness of process %d: %w", pid, err)
	}
	if self != other {
		return "", fmt.Errorf("handoff: process %d has the other bitness", pid)
	}

	var pbi processBasicInformation
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), nil); err != nil {
		return "", fmt.Errorf("handoff: basic information of %d: %w", pid, err)
	}
	if pbi.peb == 0 {
		return "", fmt.Errorf("handoff: process %d has no PEB", pid)
	}

	var params uintptr
	if err := readMemory(h, pbi.peb+pebParamsOffset, unsafe.Pointer(&params), unsafe.Sizeof(params)); err != nil {
		return "", fmt.Errorf("handoff: parameters of %d: %w", pid, err)
	}
	var dir unicodeString
	if err := readMemory(h, params+paramsCwdOffset, unsafe.Pointer(&dir), unsafe.Sizeof(dir)); err != nil {
		return "", fmt.Errorf("handoff: working directory of %d: %w", pid, err)
	}
	if dir.length == 0 || dir.buffer == 0 {
		return "", fmt.Errorf("handoff: process %d reported no working directory", pid)
	}
	text := make([]uint16, dir.length/2)
	if err := readMemory(h, dir.buffer, unsafe.Pointer(&text[0]), uintptr(len(text))*2); err != nil {
		return "", fmt.Errorf("handoff: working directory of %d: %w", pid, err)
	}
	return windows.UTF16ToString(text), nil
}

// readMemory copies size bytes at addr in another process into dst, and treats
// a short read as a failure.
func readMemory(h windows.Handle, addr uintptr, dst unsafe.Pointer, size uintptr) error {
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, (*byte)(dst), size, &n); err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("read %d of %d bytes", n, size)
	}
	return nil
}
