//go:build !darwin && !linux

package netx

import "net"

func bindSocket(uintptr, string, *net.Interface) error { return ErrUnsupported }

func PrimaryInterface() (string, error) { return "", ErrUnsupported }

func isTunDevice(ifi *net.Interface) bool { return pointToPointTun(ifi) }
