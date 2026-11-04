//go:build linux

package sidecar

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ownsPort reports whether process pid holds the loopback listening
// socket of port: the socket's inode from /proc/net/tcp must be one of
// the process's file descriptors.
func ownsPort(pid, port int) (bool, error) {
	inode, err := listenInode(port)
	if err != nil || inode == "" {
		return false, err
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false, err
	}
	want := "socket:[" + inode + "]"
	for _, fd := range fds {
		if link, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name())); err == nil && link == want {
			return true, nil
		}
	}
	return false, nil
}

// portHeld reports whether any process listens on 127.0.0.1:port right now.
// The launch asks it after a child failed to start: a port still held when
// the child is gone was taken by another program, not lost to the build.
func portHeld(port int) (bool, error) {
	inode, err := listenInode(port)
	return inode != "", err
}

// listenInode is the inode of the socket listening on 127.0.0.1:port
// ("" when none).
func listenInode(port int) (string, error) {
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		return "", err
	}
	defer f.Close()
	local := fmt.Sprintf("0100007F:%04X", port)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 9 && fields[1] == local && fields[3] == "0A" { // 0A = LISTEN
			return fields[9], nil
		}
	}
	return "", sc.Err()
}
