//go:build !server

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bernd/vibepit/experiments/desktop/backend"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

type nativeNotifications interface {
	ServiceStartup(context.Context, application.ServiceOptions) error
	ServiceShutdown() error
	CheckNotificationAuthorization() (bool, error)
	RequestNotificationAuthorization() (bool, error)
	SendNotification(notifications.NotificationOptions) error
	RemoveNotification(string) error
	RemoveDeliveredNotification(string) error
	RemoveAllDeliveredNotifications() error
	OnNotificationResponse(func(notifications.NotificationResult))
}

// Notification startup is optional: a missing desktop bus must not stop an agent.
type desktopNotifications struct {
	service nativeNotifications
	ready   atomic.Bool
}

func (n *desktopNotifications) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	if err := n.service.ServiceStartup(ctx, options); err != nil {
		log.Printf("Desktop notifications unavailable: %v", err)
		return nil
	}
	n.ready.Store(true)
	return nil
}
func (n *desktopNotifications) ServiceShutdown() error {
	if n.ready.Swap(false) {
		if err := n.service.RemoveAllDeliveredNotifications(); err != nil {
			log.Printf("Clear desktop notifications: %v", err)
		}
	}
	if err := n.service.ServiceShutdown(); err != nil {
		log.Printf("Notification shutdown: %v", err)
	}
	return nil
}

type desktopShell struct {
	app           *application.App
	workspaces    *backend.Workspaces
	notifications *desktopNotifications
	tray          *application.SystemTray
	menu          *application.Menu
	windowMenu    *application.Menu
	activeWindow  string
	windows       map[string]*application.WebviewWindow
	delivered     map[string]backend.Notice
	enabled       atomic.Bool
	stopped       atomic.Bool
	started       atomic.Bool
	queueMu       sync.Mutex
	queue         []func()
	wake          chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	refreshQueued atomic.Bool
	trayAvailable func(context.Context) bool
}

func newDesktopShell() *desktopShell {
	return &desktopShell{
		notifications: &desktopNotifications{service: newPlatformNotifications()},
		trayAvailable: platformTrayAvailable,
		windows:       make(map[string]*application.WebviewWindow), delivered: make(map[string]backend.Notice),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
}
func (s *desktopShell) services() []application.Service {
	return []application.Service{application.NewService(s.notifications)}
}
func (s *desktopShell) setup(app *application.App, workspaces *backend.Workspaces) {
	s.app, s.workspaces = app, workspaces
	s.tray = app.SystemTray.New()
	s.menu = application.NewMenu()
	if runtime.GOOS == "darwin" {
		s.tray.SetTemplateIcon(trayIcon())
	} else {
		s.tray.SetIcon(trayIcon())
	}
	s.tray.SetTooltip("Vibepit Lab")
	s.tray.SetMenu(s.menu)
	s.tray.OnClick(func() {
		s.enqueue(func() {
			if runtime.GOOS == "linux" {
				s.showAllWindows()
			} else {
				s.tray.OpenMenu()
			}
		})
	})
	app.Menu.Set(s.applicationMenu())
	workspaces.Hooks = backend.DesktopHooks{Native: true, Open: s.open, Hide: s.hide, EnableNotifications: s.enableNotifications, ReadClipboard: s.readClipboard, WriteClipboard: s.writeClipboard}
	workspaces.OnChange = s.refresh
	workspaces.OnNotice = func(notice backend.Notice) { s.enqueue(func() { s.notice(notice) }) }
	s.notifications.service.OnNotificationResponse(func(result notifications.NotificationResult) {
		s.enqueue(func() {
			if result.Error != nil {
				log.Printf("Notification response: %v", result.Error)
				return
			}
			if result.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
				return
			}
			notice, exists := s.delivered[result.Response.ID]
			if !exists {
				return
			}
			info, err := s.workspaces.GetWindow(notice.WindowID)
			if err != nil || !s.workspaces.IsNoticeActive(notice) || !notificationEligible(false, false, notice.Connection, info.Connection, notice.Deadline, time.Now()) {
				s.withdraw(notice.ID)
				return
			}
			token, _ := result.Response.UserInfo["activationToken"].(string)
			if runtime.GOOS == "linux" && token == "" {
				log.Printf("Notification click has no activation token; desktop focus protection may require another click")
			}
			if err := s.openWindowWithToken(notice.WindowID, token); err != nil {
				log.Printf("Open notification window: %v", err)
			}
		})
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		s.started.Store(true)
		s.refresh()
		go func() {
			if !s.notifications.ready.Load() {
				return
			}
			allowed, err := s.notifications.service.CheckNotificationAuthorization()
			if err != nil {
				log.Printf("Notification authorization: %v", err)
				return
			}
			// A later explicit authorization request takes precedence over this check.
			if allowed {
				s.enabled.Store(true)
			}
		}()
	})
	go s.run()
	s.refresh()
}

// Native callbacks never wait for work that might itself dispatch to the UI thread.
func (s *desktopShell) enqueue(fn func()) bool {
	s.queueMu.Lock()
	if s.stopped.Load() {
		s.queueMu.Unlock()
		return false
	}
	s.queue = append(s.queue, fn)
	s.queueMu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}
func (s *desktopShell) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case now := <-ticker.C:
			if s.stopped.Load() {
				return
			}
			s.workspaces.ExpireNotices(now)
			for id, notice := range s.delivered {
				info, err := s.workspaces.GetWindow(notice.WindowID)
				if err != nil || !s.workspaces.IsNoticeActive(notice) || !notificationEligible(false, false, notice.Connection, info.Connection, notice.Deadline, now) {
					s.withdraw(id)
				}
			}
		case <-s.wake:
			s.queueMu.Lock()
			batch := s.queue
			s.queue = nil
			s.queueMu.Unlock()
			for _, fn := range batch {
				if s.stopped.Load() {
					return
				}
				fn()
			}
		}
	}
}
func (s *desktopShell) call(fn func() error) error {
	result := make(chan error, 1)
	if !s.enqueue(func() { result <- fn() }) {
		return errors.New("application is shutting down")
	}
	select {
	case err := <-result:
		return err
	case <-s.done:
		return errors.New("application is shutting down")
	}
}
func (s *desktopShell) open(id string) error       { return s.call(func() error { return s.openWindow(id) }) }
func (s *desktopShell) openWindow(id string) error { return s.openWindowWithToken(id, "") }
func (s *desktopShell) openWindowWithToken(id, token string) error {
	info, err := s.workspaces.GetWindow(id)
	if err != nil {
		return err
	}
	window := s.windows[id]
	if window == nil {
		window = s.app.Window.NewWithOptions(application.WebviewWindowOptions{
			Name: id, Title: info.Title, Width: 1280, Height: 840, MinWidth: 800, MinHeight: 600,
			URL: info.URL, DevToolsEnabled: true,
			KeyBindings: map[string]func(application.Window){menuShortcut("q"): func(application.Window) { s.quit() }},
		})
		s.windows[id] = window
		window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
			if s.stopped.Load() {
				return
			}
			event.Cancel()
			s.enqueue(func() { s.hideNativeWindow(window) })
		})
		window.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
			s.enqueue(func() { s.activeWindow = id; s.withdrawWindow(id) })
		})
	} else {
		if err := restoreNativeWindow(window, token); err != nil {
			return err
		}
	}
	s.withdrawWindow(id)
	return nil
}
func (s *desktopShell) hide(id string) error {
	return s.call(func() error {
		if window := s.windows[id]; window != nil {
			s.hideNativeWindow(window)
			return nil
		}
		return fmt.Errorf("window %q is not open", id)
	})
}

// A missing Linux panel must not strand a running agent behind an invisible window.
// Both close and Hide RPC reach this method on the shell worker, never the UI thread.
func (s *desktopShell) hideNativeWindow(window interface {
	Hide() application.Window
	Minimise() application.Window
}) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	available := s.trayAvailable(ctx)
	if s.stopped.Load() {
		return
	}
	if available {
		window.Hide()
	} else {
		window.Minimise()
	}
}

func (s *desktopShell) showAllWindows() {
	for _, info := range s.workspaces.ListWindows() {
		if err := s.openWindow(info.ID); err != nil {
			log.Printf("Show window: %v", err)
		}
	}
}

func (s *desktopShell) quit() {
	s.enqueue(func() {
		for id := range s.delivered {
			s.withdraw(id)
		}
		s.shutdown()
		go s.app.Quit()
	})
}

// Authorization can keep an OS prompt open for minutes; do not block window actions.
func (s *desktopShell) enableNotifications() (string, error) {
	if s.stopped.Load() {
		return "", errors.New("application is shutting down")
	}
	if !s.notifications.ready.Load() {
		return "Notifications unavailable on this desktop", nil
	}
	allowed, err := s.notifications.service.RequestNotificationAuthorization()
	if err != nil {
		log.Printf("Enable notifications: %v", err)
		return "Notifications unavailable", nil
	}
	s.enabled.Store(allowed)
	if allowed {
		return "Notifications enabled", nil
	}
	return "Notifications denied; enable them in system settings", nil
}
func (s *desktopShell) notice(notice backend.Notice) {
	if notice.Kind == "withdraw" {
		s.withdraw(notice.ID)
		return
	}
	if !s.enabled.Load() || !s.notifications.ready.Load() {
		return
	}
	if _, exists := s.delivered[notice.ID]; exists {
		return
	}
	window := s.windows[notice.WindowID]
	info, err := s.workspaces.GetWindow(notice.WindowID)
	if err != nil || window == nil || !s.workspaces.IsNoticeActive(notice) || !notificationEligible(window.IsFocused(), info.NotifyWhenFocused, notice.Connection, info.Connection, notice.Deadline, time.Now()) {
		return
	}
	err = s.notifications.service.SendNotification(notifications.NotificationOptions{
		ID: notice.ID, Title: notice.Title, Body: notice.Body, ThreadID: notice.WindowID,
	})
	if err != nil {
		log.Printf("Desktop notification: %v", err)
		return
	}
	s.delivered[notice.ID] = notice
}
func (s *desktopShell) withdraw(id string) {
	if _, exists := s.delivered[id]; !exists {
		return
	}
	delete(s.delivered, id)
	if !s.notifications.ready.Load() {
		return
	}
	remove := s.notifications.service.RemoveNotification
	if runtime.GOOS == "darwin" {
		remove = s.notifications.service.RemoveDeliveredNotification
	}
	if err := remove(id); err != nil {
		log.Printf("Withdraw notification: %v", err)
	}
}
func (s *desktopShell) withdrawWindow(id string) {
	for noticeID, notice := range s.delivered {
		if notice.WindowID == id {
			s.withdraw(noticeID)
		}
	}
}
func (s *desktopShell) refresh() {
	if !s.started.Load() || !s.refreshQueued.CompareAndSwap(false, true) {
		return
	}
	s.enqueue(func() {
		s.refreshQueued.Store(false)
		application.InvokeSync(func() {
			if s.stopped.Load() {
				return
			}
			s.refreshWindowMenu()
			s.menu.Clear()
			s.menu.Add("New window").OnClick(func(*application.Context) {
				go func() {
					if _, err := s.workspaces.NewWindow(); err != nil {
						log.Printf("New window: %v", err)
					}
				}()
			})
			s.menu.AddSeparator()
			for _, info := range s.workspaces.ListWindows() {
				id := info.ID
				label := strings.TrimSpace(info.Title)
				if label == "" {
					label = "Vibepit Lab"
				}
				status := info.AgentStatus
				if !info.Connected {
					status = "disconnected"
				}
				if status != "" {
					label += " · " + status
				}
				if info.Pending > 0 {
					label += fmt.Sprintf(" · %d waiting", info.Pending)
				}
				s.menu.Add(label).OnClick(func(*application.Context) {
					s.enqueue(func() {
						if err := s.openWindow(id); err != nil {
							log.Printf("Open window: %v", err)
						}
					})
				})
				if window := s.windows[id]; window != nil {
					window.SetTitle(info.Title)
				}
			}
			s.menu.AddSeparator()
			s.menu.Add("Show all windows").OnClick(func(*application.Context) { s.enqueue(s.showAllWindows) })
			s.menu.Add("Enable notifications").OnClick(func(*application.Context) {
				go func() { status, err := s.enableNotifications(); log.Printf("%s %v", status, err) }()
			})
			s.menu.AddSeparator()
			s.menu.Add("Quit Vibepit Lab").OnClick(func(*application.Context) { s.quit() })
			s.menu.Update()
			// Linux exports a separate D-Bus menu snapshot, refreshed by SetMenu.
			s.tray.SetMenu(s.menu)
		})
	})
}
func (s *desktopShell) shutdown() {
	s.stopOnce.Do(func() { s.stopped.Store(true); close(s.done) })
}

func trayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	ink := color.NRGBA{R: 30, G: 30, B: 30, A: 255}
	for y := 4; y < 28; y++ {
		for x := 3; x < 29; x++ {
			if x < 5 || x > 26 || y < 6 || y > 25 {
				img.SetNRGBA(x, y, ink)
			}
		}
	}
	for i := 0; i < 6; i++ {
		for d := 0; d < 2; d++ {
			img.SetNRGBA(8+i, 10+i+d, ink)
			img.SetNRGBA(8+i, 20-i+d, ink)
		}
	}
	for x := 17; x < 24; x++ {
		img.SetNRGBA(x, 21, ink)
		img.SetNRGBA(x, 22, ink)
	}
	var data bytes.Buffer
	_ = png.Encode(&data, img)
	return data.Bytes()
}
