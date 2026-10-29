// Package profile ensures that one core owns a demo profile at a time.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrInUse = errors.New("demo profile is already in use")

type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire keeps the lock file open; its existence alone never indicates ownership.
func Acquire(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".core.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("demo profile is already in use or cannot be locked: %w", err)
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}
