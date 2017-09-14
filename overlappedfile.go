package main

import (
	"io"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getOverlappedResultFunc = windows.MustLoadDLL("kernel32.dll").MustFindProc("GetOverlappedResult")

type overlappedFile struct {
	h windows.Handle
	m sync.Mutex
	e []windows.Handle
}

func (f *overlappedFile) getEvent() windows.Handle {
	f.m.Lock()
	if len(f.e) == 0 {
		f.m.Unlock()
		e, err := windows.CreateEvent(nil, 0, 0, nil)
		if err != nil {
			panic(err)
		}
		return e
	}
	e := f.e[len(f.e)-1]
	f.e = f.e[:len(f.e)-1]
	f.m.Unlock()
	return e
}

func (f *overlappedFile) putEvent(e windows.Handle) {
	windows.ResetEvent(e)
	f.m.Lock()
	f.e = append(f.e, e)
	f.m.Unlock()
}

func (f *overlappedFile) asyncIo(fn func(h windows.Handle, n *uint32, o *windows.Overlapped) error) (uint32, error) {
	o := &windows.Overlapped{}
	e := f.getEvent()
	defer f.putEvent(e)
	o.HEvent = e
	var n uint32
	err := fn(f.h, &n, o)
	if err == windows.ERROR_IO_PENDING {
		r, _, err := getOverlappedResultFunc.Call(uintptr(f.h), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(&n)), 1)
		if r == 0 {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	return n, nil
}

func (f *overlappedFile) Read(b []byte) (int, error) {
	n, err := f.asyncIo(func(h windows.Handle, n *uint32, o *windows.Overlapped) error {
		return windows.ReadFile(h, b, n, o)
	})
	err = os.NewSyscallError("read", err)
	if err == nil && n == 0 && len(b) > 0 {
		err = io.EOF
	}
	return int(n), err
}

func (f *overlappedFile) Write(b []byte) (int, error) {
	n, err := f.asyncIo(func(h windows.Handle, n *uint32, o *windows.Overlapped) error {
		return windows.WriteFile(h, b, n, o)
	})
	return int(n), os.NewSyscallError("write", err)
}

func (f *overlappedFile) Close() error {
	err := windows.Close(f.h)
	if err != nil {
		panic(err)
	}
	f.h = 0
	for _, h := range f.e {
		err := windows.Close(h)
		if err != nil {
			panic(err)
		}
	}
	f.e = nil
	return nil
}

func newOverlappedFile(h windows.Handle) *overlappedFile {
	return &overlappedFile{h: h}
}
