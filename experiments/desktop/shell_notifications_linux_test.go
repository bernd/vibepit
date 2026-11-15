//go:build linux && !server

package main

import (
	"bufio"
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// A private bus exercises the actual notification transport without a GUI or
// sending test notifications to the developer's desktop.
func TestLinuxNotificationClickCarriesActivationToken(t *testing.T) {
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("private D-Bus test requires dbus-daemon")
	}
	command := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	output, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(output)
	require.True(t, scanner.Scan())
	address := scanner.Text()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	server, err := dbus.Connect(address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	daemon := &testNotificationDaemon{conn: server, ids: make(chan uint32, 10)}
	require.NoError(t, server.Export(daemon, notificationBusPath, notificationBusName))
	reply, err := server.RequestName(notificationBusName, dbus.NameFlagDoNotQueue)
	require.NoError(t, err)
	require.Equal(t, dbus.RequestNameReplyPrimaryOwner, reply)
	n := newPlatformNotifications()
	appContext, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	require.NoError(t, n.ServiceStartup(appContext, application.ServiceOptions{}))
	t.Cleanup(func() { require.NoError(t, n.ServiceShutdown()) })
	results := make(chan notifications.NotificationResult, 10)
	n.OnNotificationResponse(func(r notifications.NotificationResult) { results <- r })
	require.NoError(t, n.SendNotification(notifications.NotificationOptions{ID: "window-one/approval", Title: "Approval"}))
	one := <-daemon.ids
	require.NoError(t, n.SendNotification(notifications.NotificationOptions{ID: "window-two/approval", Title: "Approval"}))
	two := <-daemon.ids
	emit := func(member string, args ...any) {
		require.NoError(t, server.Emit(notificationBusPath, notificationBusName+"."+member, args...))
	}
	emit("ActivationToken", one, "token-for-one")
	emit("ActivationToken", two, "token-for-two")
	emit("ActionInvoked", two, "default")
	select {
	case result := <-results:
		assert.Equal(t, "window-two/approval", result.Response.ID)
		assert.Equal(t, notifications.DefaultActionIdentifier, result.Response.ActionIdentifier)
		assert.Equal(t, "token-for-two", result.Response.UserInfo["activationToken"])
	case <-time.After(2 * time.Second):
		t.Fatal("notification click was not delivered")
	}
	emit("ActionInvoked", one, "default")
	select {
	case result := <-results:
		assert.Equal(t, "window-one/approval", result.Response.ID)
		assert.Equal(t, "token-for-one", result.Response.UserInfo["activationToken"])
	case <-time.After(2 * time.Second):
		t.Fatal("second click was not delivered")
	}
	// A token is single-use; duplicate clicks and withdrawn notifications are ignored.
	emit("ActionInvoked", one, "default")
	require.NoError(t, n.SendNotification(notifications.NotificationOptions{ID: "withdrawn", Title: "Approval"}))
	withdrawn := <-daemon.ids
	require.NoError(t, n.RemoveNotification("withdrawn"))
	emit("ActivationToken", withdrawn, "stale-token")
	emit("ActionInvoked", withdrawn, "default")
	// A subsequent valid click is a signal-order barrier for the assertions above.
	require.NoError(t, n.SendNotification(notifications.NotificationOptions{ID: "no-token", Title: "Done"}))
	noToken := <-daemon.ids
	emit("ActionInvoked", noToken, "default")
	select {
	case result := <-results:
		assert.Equal(t, "no-token", result.Response.ID)
		assert.Empty(t, result.Response.UserInfo["activationToken"])
	case <-time.After(2 * time.Second):
		t.Fatal("click without a token was not delivered")
	}
	assert.Empty(t, results)
	// Wails cancels its application context before shutting services down. The
	// notification bus must remain usable long enough to withdraw remaining toasts.
	require.NoError(t, n.SendNotification(notifications.NotificationOptions{ID: "shutdown", Title: "Pending"}))
	<-daemon.ids
	cancelApp()
	require.NoError(t, n.RemoveAllDeliveredNotifications())
	linux := n.(*linuxNotifications)
	linux.mu.Lock()
	assert.Empty(t, linux.pending)
	linux.mu.Unlock()
}

func TestLinuxNotificationRejectsSignalsFromAnotherSender(t *testing.T) {
	n := newPlatformNotifications().(*linuxNotifications)
	n.owner = ":1.10"
	n.pending[1] = linuxNotice{id: "approval"}
	var results []notifications.NotificationResult
	n.OnNotificationResponse(func(result notifications.NotificationResult) { results = append(results, result) })
	n.handleSignal(&dbus.Signal{Sender: ":1.11", Path: notificationBusPath, Name: notificationBusName + ".ActivationToken", Body: []any{uint32(1), "wrong"}})
	n.handleSignal(&dbus.Signal{Sender: ":1.11", Path: notificationBusPath, Name: notificationBusName + ".ActionInvoked", Body: []any{uint32(1), "default"}})
	assert.Empty(t, results)
	n.handleSignal(&dbus.Signal{Sender: n.owner, Path: notificationBusPath, Name: notificationBusName + ".ActionInvoked", Body: []any{uint32(1), "default"}})
	require.Len(t, results, 1)
	assert.Empty(t, results[0].Response.UserInfo["activationToken"])
}

type testNotificationDaemon struct {
	conn *dbus.Conn
	mu   sync.Mutex
	next uint32
	ids  chan uint32
}

func (d *testNotificationDaemon) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body"}, nil
}
func (d *testNotificationDaemon) Notify(_ string, replaces uint32, _ string, _ string, _ string, _ []string, _ map[string]dbus.Variant, _ int32) (uint32, *dbus.Error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := replaces
	if id == 0 {
		d.next++
		id = d.next
	}
	d.ids <- id
	return id, nil
}
func (d *testNotificationDaemon) CloseNotification(id uint32) *dbus.Error {
	if err := d.conn.Emit(notificationBusPath, notificationBusName+".NotificationClosed", id, uint32(3)); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}
