//go:build !windows

package main

func platformShutdown() <-chan struct{} { return nil }
