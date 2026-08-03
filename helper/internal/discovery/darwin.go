//go:build darwin && cgo

package discovery

/*
#cgo LDFLAGS: -lproc
#include <arpa/inet.h>
#include <libproc.h>
#include <stdint.h>
#include <stdlib.h>
#include <sys/proc_info.h>
#include <sys/sysctl.h>
#include <sys/types.h>

static int portico_list_pids(uid_t uid, int *buffer, int bytes) {
	return proc_listpids(PROC_UID_ONLY, (uint32_t)uid, buffer, bytes);
}

static int portico_list_fds(int pid, struct proc_fdinfo *buffer, int bytes) {
	return proc_pidinfo(pid, PROC_PIDLISTFDS, 0, buffer, bytes);
}

struct portico_process_identity {
	uid_t uid;
	uint64_t start_seconds;
	uint64_t start_microseconds;
};

static int portico_process_identity(int pid, struct portico_process_identity *identity) {
	struct proc_bsdinfo info;
	int size = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (size != sizeof(info)) {
		return 0;
	}
	identity->uid = info.pbi_uid;
	identity->start_seconds = info.pbi_start_tvsec;
	identity->start_microseconds = info.pbi_start_tvusec;
	return 1;
}

static int portico_tcp_listen_port(int pid, int fd, uint16_t *port) {
	struct socket_fdinfo info;
	int size = proc_pidfdinfo(pid, fd, PROC_PIDFDSOCKETINFO, &info, sizeof(info));
	if (size != sizeof(info) || info.psi.soi_kind != SOCKINFO_TCP ||
		info.psi.soi_proto.pri_tcp.tcpsi_state != TSI_S_LISTEN) {
		return 0;
	}
	*port = ntohs((uint16_t)info.psi.soi_proto.pri_tcp.tcpsi_ini.insi_lport);
	return *port != 0;
}

static int portico_process_args(int pid, void *buffer, size_t *size) {
	int mib[3] = {CTL_KERN, KERN_PROCARGS2, pid};
	return sysctl(mib, 3, buffer, size, NULL, 0);
}
*/
import "C"

import (
	"context"
	"encoding/binary"
	"os"
	"unsafe"
)

const maxProcessArgsBuffer = 1 << 20

type darwinSource struct{}

func New() *Discoverer {
	return newDiscoverer(darwinSource{}, loopbackProber{})
}

func (darwinSource) listeners(ctx context.Context) ([]Candidate, error) {
	uid := C.uid_t(os.Getuid())
	pids, ok := currentUserPIDs(uid)
	if !ok {
		return nil, errDiscoveryUnavailable
	}
	result := make([]Candidate, 0)
	for _, pid := range pids {
		if ctx.Err() != nil {
			return nil, errDiscoveryUnavailable
		}
		identity, ok := processIdentity(pid)
		if !ok || identity.uid != uid {
			continue
		}
		ports := listeningPorts(pid)
		if len(ports) == 0 {
			continue
		}
		executable, argv := processArguments(pid)
		if current, ok := processIdentity(pid); !ok || current != identity {
			continue
		}
		for _, port := range ports {
			result = append(result, reduce(port, executable, argv))
		}
	}
	return result, nil
}

func currentUserPIDs(uid C.uid_t) ([]C.int, bool) {
	bytesNeeded := int(C.portico_list_pids(uid, nil, 0))
	if bytesNeeded <= 0 {
		return nil, false
	}
	count := bytesNeeded/int(C.sizeof_int) + 64
	buffer := make([]C.int, count)
	written := int(C.portico_list_pids(uid, &buffer[0], C.int(len(buffer)*int(C.sizeof_int))))
	if written <= 0 {
		return nil, false
	}
	buffer = buffer[:min(len(buffer), written/int(C.sizeof_int))]
	result := buffer[:0]
	for _, pid := range buffer {
		if pid > 0 {
			result = append(result, pid)
		}
	}
	return result, true
}

type processIdentityValue struct {
	uid               C.uid_t
	startSeconds      C.uint64_t
	startMicroseconds C.uint64_t
}

func processIdentity(pid C.int) (processIdentityValue, bool) {
	var identity C.struct_portico_process_identity
	if C.portico_process_identity(pid, &identity) == 0 {
		return processIdentityValue{}, false
	}
	return processIdentityValue{
		uid:               identity.uid,
		startSeconds:      identity.start_seconds,
		startMicroseconds: identity.start_microseconds,
	}, true
}

func listeningPorts(pid C.int) []uint16 {
	bytesNeeded := int(C.portico_list_fds(pid, nil, 0))
	if bytesNeeded <= 0 {
		return nil
	}
	count := bytesNeeded/int(C.sizeof_struct_proc_fdinfo) + 32
	buffer := make([]C.struct_proc_fdinfo, count)
	written := int(C.portico_list_fds(pid, &buffer[0], C.int(len(buffer)*int(C.sizeof_struct_proc_fdinfo))))
	if written <= 0 {
		return nil
	}
	buffer = buffer[:min(len(buffer), written/int(C.sizeof_struct_proc_fdinfo))]
	ports := make([]uint16, 0)
	for _, descriptor := range buffer {
		if descriptor.proc_fdtype != C.PROX_FDTYPE_SOCKET {
			continue
		}
		var port C.uint16_t
		if C.portico_tcp_listen_port(pid, descriptor.proc_fd, &port) != 0 {
			ports = append(ports, uint16(port))
		}
	}
	return ports
}

func processArguments(pid C.int) (string, []string) {
	var size C.size_t
	if C.portico_process_args(pid, nil, &size) != 0 || size <= C.size_t(C.sizeof_int) || size > maxProcessArgsBuffer {
		return "", nil
	}
	buffer := make([]byte, int(size))
	if C.portico_process_args(pid, unsafe.Pointer(&buffer[0]), &size) != 0 || size <= C.size_t(C.sizeof_int) {
		return "", nil
	}
	buffer = buffer[:int(size)]
	argc := int(int32(binary.NativeEndian.Uint32(buffer[:C.sizeof_int])))
	if argc <= 0 || argc > len(buffer) {
		return "", nil
	}
	position := int(C.sizeof_int)
	executable, position, ok := readNullTerminated(buffer, position)
	if !ok {
		return "", nil
	}
	for position < len(buffer) && buffer[position] == 0 {
		position++
	}
	argv := make([]string, 0, argc)
	for len(argv) < argc && position < len(buffer) {
		argument, next, valid := readNullTerminated(buffer, position)
		if !valid {
			break
		}
		argv = append(argv, argument)
		position = next
	}
	return executable, argv
}

func readNullTerminated(buffer []byte, start int) (string, int, bool) {
	for end := start; end < len(buffer); end++ {
		if buffer[end] == 0 {
			return string(buffer[start:end]), end + 1, true
		}
	}
	return "", start, false
}
