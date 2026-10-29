package transport

import (
	"errors"

	"golang.org/x/sys/windows"
)

type processObserver struct{ handle windows.Handle }

func observeProcess(pid int) (*processObserver, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &processObserver{handle: handle}, nil
}

func (p *processObserver) Alive() (bool, error) {
	result, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return false, err
	}
	if result == windows.WAIT_OBJECT_0 {
		return false, nil
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return true, nil
	}
	return false, errors.New("unexpected process wait result")
}

func (p *processObserver) Terminate() error { return windows.TerminateProcess(p.handle, 1) }

func (p *processObserver) Wait() error {
	result, err := windows.WaitForSingleObject(p.handle, 8000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("process did not exit")
	}
	return nil
}

func (p *processObserver) Close() { _ = windows.CloseHandle(p.handle) }
