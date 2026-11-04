//go:build windows

package sidecar

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// TCP table constants (iphlpapi.h).
const (
	afInet                   = 2
	tcpTableOwnerPIDListener = 3
	tcpRowBytes              = 24 // MIB_TCPROW_OWNER_PID: six DWORDs
)

// listenerOwner is the one walk of the TCP listener table: the pid holding
// the 127.0.0.1 listening socket of port, and whether such a socket exists.
func listenerOwner(port int) (pid int, held bool, err error) {
	table, err := listeners()
	if err != nil {
		return 0, false, err
	}
	n := int(binary.LittleEndian.Uint32(table))
	for i := range n {
		row := table[4+i*tcpRowBytes : 4+(i+1)*tcpRowBytes]
		addr := binary.LittleEndian.Uint32(row[4:8]) // network order bytes as read
		p := int(row[8])<<8 | int(row[9])            // dwLocalPort: network byte order
		if p == port && (addr == 0x0100007F || addr == 0) {
			return int(binary.LittleEndian.Uint32(row[20:24])), true, nil
		}
	}
	return 0, false, nil
}

// ownsPort reports whether process pid holds the 127.0.0.1 listening
// socket of port (GetExtendedTcpTable, TCP_TABLE_OWNER_PID_LISTENER).
func ownsPort(pid, port int) (bool, error) {
	owner, held, err := listenerOwner(port)
	return held && owner == pid, err
}

// portHeld reports whether any process listens on 127.0.0.1:port right now.
// The launch asks it after a child failed to start: a port still held when
// the child is gone was taken by another program, not lost to the build.
func portHeld(port int) (bool, error) {
	_, held, err := listenerOwner(port)
	return held, err
}

func listeners() ([]byte, error) {
	size := uint32(64 << 10)
	for range 4 {
		buf := make([]byte, size)
		r, _, _ := getExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDListener, 0)
		switch windows.Errno(r) {
		case 0:
			return buf, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue // size now holds what is needed
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
	}
	return nil, fmt.Errorf("GetExtendedTcpTable: the table keeps growing")
}
