//go:build !darwin

package main

import "time"

func (e *env) startMacOSVM(_ osDef, _ string, _ time.Duration) error { return requireMac() }
