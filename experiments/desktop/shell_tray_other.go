//go:build !linux && !server

package main

import "context"

func platformTrayAvailable(context.Context) bool { return true }
