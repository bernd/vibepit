//go:build linux && !server

package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

const notificationBusName = "org.freedesktop.Notifications"
const notificationBusPath dbus.ObjectPath = "/org/freedesktop/Notifications"

// Wails 3.1 drops ActivationToken signals. Keep the click and its token on the
// same D-Bus connection so GNOME can authorise the requested window activation.
// This adapter implements only the immediate notifications used by this spike.
type linuxNotifications struct {
	mu       sync.Mutex
	conn     *dbus.Conn
	owner    string
	pending  map[uint32]linuxNotice
	callback func(notifications.NotificationResult)
}
type linuxNotice struct{ id, token string }

func newPlatformNotifications() nativeNotifications {
	return &linuxNotifications{pending: make(map[uint32]linuxNotice)}
}
func (n *linuxNotifications) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	conn, err := dbus.ConnectSessionBus(dbus.WithSignalHandler(dbus.NewSequentialSignalHandler()))
	if err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 64)
	conn.Signal(signals)
	setupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = conn.AddMatchSignalContext(setupCtx, dbus.WithMatchSender(notificationBusName), dbus.WithMatchObjectPath(notificationBusPath), dbus.WithMatchInterface(notificationBusName)); err != nil {
		conn.Close()
		return err
	}
	var capabilities []string
	if err = conn.Object(notificationBusName, notificationBusPath).CallWithContext(setupCtx, notificationBusName+".GetCapabilities", 0).Store(&capabilities); err != nil {
		conn.Close()
		return err
	}
	if err = conn.BusObject().CallWithContext(setupCtx, "org.freedesktop.DBus.GetNameOwner", 0, notificationBusName).Store(&n.owner); err != nil {
		conn.Close()
		return err
	}
	n.conn = conn
	go func() {
		for signal := range signals {
			n.handleSignal(signal)
		}
	}()
	return nil
}
func (n *linuxNotifications) ServiceShutdown() error {
	if n.conn != nil {
		return n.conn.Close()
	}
	return nil
}
func (n *linuxNotifications) RequestNotificationAuthorization() (bool, error) {
	return n.CheckNotificationAuthorization()
}
func (n *linuxNotifications) CheckNotificationAuthorization() (bool, error) {
	if n.conn == nil {
		return false, errors.New("desktop notification service unavailable")
	}
	return true, nil
}
func (n *linuxNotifications) OnNotificationResponse(callback func(notifications.NotificationResult)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.callback = callback
}
func (n *linuxNotifications) SendNotification(options notifications.NotificationOptions) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		return errors.New("desktop notification service unavailable")
	}
	var replaces uint32
	for id, notice := range n.pending {
		if notice.id == options.ID {
			replaces = id
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var owner string
	if err := n.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, notificationBusName).Store(&owner); err != nil {
		return err
	}
	if owner != n.owner {
		n.pending = make(map[uint32]linuxNotice)
		n.owner = owner
		replaces = 0
	}
	var id uint32
	err := n.conn.Object(owner, notificationBusPath).CallWithContext(ctx, notificationBusName+".Notify", 0,
		"Vibepit Lab", replaces, "", options.Title, html.EscapeString(options.Body), []string{"default", "Open Vibepit"}, map[string]dbus.Variant{}, int32(-1),
	).Store(&id)
	if err != nil {
		return fmt.Errorf("send desktop notification: %w", err)
	}
	if replaces != 0 {
		delete(n.pending, replaces)
	}
	// Holding mu across Notify makes signal processing wait until the returned ID
	// is registered, even when a desktop responds and emits signals immediately.
	n.pending[id] = linuxNotice{id: options.ID}
	return nil
}
func (n *linuxNotifications) RemoveNotification(id string) error {
	n.mu.Lock()
	owner := n.owner
	var busID uint32
	for key, notice := range n.pending {
		if notice.id == id {
			busID = key
			delete(n.pending, key)
			break
		}
	}
	n.mu.Unlock()
	if busID == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return n.conn.Object(owner, notificationBusPath).CallWithContext(ctx, notificationBusName+".CloseNotification", 0, busID).Err
}
func (n *linuxNotifications) RemoveDeliveredNotification(id string) error {
	return n.RemoveNotification(id)
}
func (n *linuxNotifications) RemoveAllDeliveredNotifications() error {
	n.mu.Lock()
	ids := make([]string, 0, len(n.pending))
	for _, notice := range n.pending {
		ids = append(ids, notice.id)
	}
	n.mu.Unlock()
	var errs []error
	for _, id := range ids {
		errs = append(errs, n.RemoveNotification(id))
	}
	return errors.Join(errs...)
}
func (n *linuxNotifications) handleSignal(signal *dbus.Signal) {
	if signal == nil || signal.Path != notificationBusPath || len(signal.Body) < 2 {
		return
	}
	id, ok := signal.Body[0].(uint32)
	if !ok {
		return
	}
	n.mu.Lock()
	if signal.Sender != n.owner {
		n.mu.Unlock()
		return
	}
	notice, exists := n.pending[id]
	if !exists {
		n.mu.Unlock()
		return
	}
	var result *notifications.NotificationResult
	switch signal.Name {
	case notificationBusName + ".ActivationToken":
		if token, ok := signal.Body[1].(string); ok {
			notice.token = token
			n.pending[id] = notice
		}
	case notificationBusName + ".ActionInvoked":
		if action, ok := signal.Body[1].(string); ok && action == "default" {
			delete(n.pending, id)
			result = &notifications.NotificationResult{Response: notifications.NotificationResponse{ID: notice.id, ActionIdentifier: notifications.DefaultActionIdentifier, UserInfo: map[string]any{"activationToken": notice.token}}}
		}
	case notificationBusName + ".NotificationClosed":
		delete(n.pending, id)
	}
	callback := n.callback
	n.mu.Unlock()
	if result != nil && callback != nil {
		callback(*result)
	}
}
