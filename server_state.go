package main

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// An AtomicInt is an int64 to be accessed atomically.
type AtomicInt int64

// Add atomically adds n to i.
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get atomically gets the value of i.
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

type ServerState struct {
	sync.Mutex
	closed         bool
	ActiveDownload AtomicInt
}

func (s *ServerState) addActiveDownload(n int) {
	s.Lock()
	defer s.Unlock()
	s.ActiveDownload.Add(int64(n))
}

func (s *ServerState) Close() error {
	s.closed = true
	if wsclient != nil {
		wsclient.Close()
	}
	time.Sleep(time.Millisecond * 500) // 0.5s
	for {
		if s.ActiveDownload == 0 { // Wait until all download finished
			break
		}
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

func (s *ServerState) IsClosed() bool {
	return s.closed
}
