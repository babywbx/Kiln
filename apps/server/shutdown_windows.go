//go:build windows

package main

import "sync"

var (
	serviceStopOnce sync.Once
	serviceStopCh   = make(chan struct{})
)

func platformShutdown() <-chan struct{} { return serviceStopCh }

func requestServiceStop() { serviceStopOnce.Do(func() { close(serviceStopCh) }) }
