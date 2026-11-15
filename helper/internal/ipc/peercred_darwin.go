package ipc

/*
#include <sys/types.h>
#include <unistd.h>
*/
import "C"

import "errors"

func platformPeerUID(fd int) (int, error) {
	var uid C.uid_t
	var gid C.gid_t
	if C.getpeereid(C.int(fd), &uid, &gid) != 0 {
		return -1, errors.New("getpeereid failed")
	}
	return int(uid), nil
}
