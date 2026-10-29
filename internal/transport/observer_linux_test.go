package transport

import (
	"errors"

	"golang.org/x/sys/unix"
)

type processObserver struct{ fd int }

func observeProcess(pid int) (*processObserver, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &processObserver{fd: fd}, nil
}

func (p *processObserver) Alive() (bool, error) {
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	count, err := unix.Poll(fds, 0)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (p *processObserver) Terminate() error { return unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0) }

func (p *processObserver) Wait() error {
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	count, err := unix.Poll(fds, 8000)
	if err != nil {
		return err
	}
	if count == 0 || fds[0].Revents&unix.POLLIN == 0 {
		return errors.New("process did not exit")
	}
	return nil
}

func (p *processObserver) Close() { _ = unix.Close(p.fd) }
