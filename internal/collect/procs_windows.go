//go:build windows

package collect

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// listProcs reads the Windows process table through the native API (ADR-0023).
//
// ADR-0012 deferred this and named Toolhelp32 as the way in. Toolhelp32 alone
// turned out not to be enough: it reports the image name and nothing else,
// while `codex app-server` and an interactive `codex` are the same codex.exe,
// and ADR-0012 requires telling them apart. Only the command line does that, so
// each process is opened and its PEB read for one.
//
// The alternative — asking WMI, via PowerShell or wmic — was measured on mypc
// and rejected: `Get-CimInstance Win32_Process` took 4.5s for 449 processes,
// against a collection interval of 5s. This path is a few syscalls per process.
func listProcs() ([]procInfo, error) {
	pids, err := snapshotPIDs()
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, 16)
	for _, pid := range pids {
		info, ok := inspect(pid)
		if !ok {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// snapshotPIDs walks a Toolhelp32 snapshot for the live PIDs.
//
// Only the PID is taken from it. The image name it also carries is exactly the
// field that cannot answer the question this package asks.
func snapshotPIDs() ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var pids []uint32
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID != 0 {
			pids = append(pids, entry.ProcessID)
		}
	}
	if err != nil && err != windows.ERROR_NO_MORE_FILES {
		return nil, err
	}
	return pids, nil
}

// inspect reads one process's command line and start time, reporting false when
// either is unavailable.
//
// Dropping a process this cannot read is deliberate rather than a shortcut. The
// fallback would be the image name from the snapshot, and a bare `codex.exe`
// cannot be distinguished from `codex.exe app-server` — it would be reported as
// a session that is not there, permanently, since app-servers outlive every
// real session. A missed session corrects itself on the next poll five seconds
// later; a phantom one never does.
//
// What actually gets dropped here is system and other-user processes, which the
// agent has no business reading and which are never an agent CLI the user
// started. A 32-bit process would be dropped too: its PEB has a different
// layout, and no agent CLI ships as one.
func inspect(pid uint32) (procInfo, bool) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return procInfo{}, false
	}
	defer windows.CloseHandle(handle)

	command, err := commandLine(handle)
	if err != nil || command == "" {
		return procInfo{}, false
	}
	info := procInfo{pid: int(pid), command: command}
	// A process with no readable start time is still a running session; the
	// panel just cannot say how long it has been up.
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
		info.startedAt = creation.Nanoseconds() / 1e6
	}
	return info, true
}

// commandLine follows PEB → ProcessParameters → CommandLine in the target
// process's address space.
//
// Every hop is a ReadProcessMemory rather than a dereference: these are
// pointers into another process, and the structs only describe the shape of
// what is there.
func commandLine(handle windows.Handle) (string, error) {
	var basic windows.PROCESS_BASIC_INFORMATION
	var returned uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation,
		unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), &returned); err != nil {
		return "", err
	}
	var peb windows.PEB
	if err := readMemory(handle, uintptr(unsafe.Pointer(basic.PebBaseAddress)),
		unsafe.Pointer(&peb), unsafe.Sizeof(peb)); err != nil {
		return "", err
	}
	var params windows.RTL_USER_PROCESS_PARAMETERS
	if err := readMemory(handle, uintptr(unsafe.Pointer(peb.ProcessParameters)),
		unsafe.Pointer(&params), unsafe.Sizeof(params)); err != nil {
		return "", err
	}
	// Length is in bytes and the buffer is UTF-16, so an odd length or one
	// past any plausible command line means the read landed somewhere wrong.
	const maxCommandLine = 64 * 1024
	length := int(params.CommandLine.Length)
	if length == 0 || length%2 != 0 || length > maxCommandLine {
		return "", nil
	}
	buffer := make([]uint16, length/2)
	if err := readMemory(handle, uintptr(unsafe.Pointer(params.CommandLine.Buffer)),
		unsafe.Pointer(&buffer[0]), uintptr(length)); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer), nil
}

// readMemory is ReadProcessMemory with a short read treated as a failure: a
// partially filled struct would be read as pointers into nowhere.
func readMemory(handle windows.Handle, address uintptr, dest unsafe.Pointer, size uintptr) error {
	if address == 0 {
		return windows.ERROR_INVALID_ADDRESS
	}
	var read uintptr
	if err := windows.ReadProcessMemory(handle, address, (*byte)(dest), size, &read); err != nil {
		return err
	}
	if read != size {
		return windows.ERROR_PARTIAL_COPY
	}
	return nil
}
