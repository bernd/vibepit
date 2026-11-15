//go:build linux && !server

package main

import (
	"context"

	"github.com/godbus/dbus/v5"
)

// A watcher may exist before any panel has registered to display its icons.
func platformTrayAvailable(ctx context.Context) bool {
	result := make(chan bool, 1)
	go func() {
		conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
		if err != nil {
			result <- false
			return
		}
		defer conn.Close()
		var value dbus.Variant
		err = conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher").CallWithContext(
			ctx, "org.freedesktop.DBus.Properties.Get", dbus.FlagNoAutoStart,
			"org.kde.StatusNotifierWatcher", "IsStatusNotifierHostRegistered",
		).Store(&value)
		available, ok := value.Value().(bool)
		result <- err == nil && ok && available
	}()
	select {
	case available := <-result:
		return available
	case <-ctx.Done():
		return false
	}
}
