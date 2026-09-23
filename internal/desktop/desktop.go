// Package desktop presents the agent's saved state through Freedesktop
// notifications. It owns no update state and never activates a system itself.
package desktop

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/esiqveland/notify"
	"github.com/godbus/dbus/v5"
	"github.com/nlewo/comin/pkg/client"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/sirupsen/logrus"
)

type agent interface {
	GetManagerStateContext(context.Context) (*protobuf.State, error)
	ConfirmContext(context.Context, string, string) error
}

type screen struct {
	api        agent
	notifier   notify.Notifier
	title      string
	id         uint32
	uuid       string
	dismissed  string
	available  string
	lastResult string
	russian    bool
}

func (s *screen) text(en, ru string) string {
	if s.russian {
		return ru
	}
	return en
}

func pending(state *protobuf.State) *protobuf.Generation {
	// Interactive deployment is explicitly manual. Build confirmation and
	// automatic deployment retain their separate agent policies.
	if state.GetDeployConfirmer().GetMode() != 0 || state.GetDeployer().GetIsDeploying().GetValue() {
		return nil
	}
	uuid := state.GetDeployConfirmer().GetSubmitted()
	for _, g := range state.GetStore().GetGenerations() {
		if uuid != "" && g.Uuid == uuid && g.BuildStatus == "built" && g.BuildErr == "" {
			return g
		}
	}
	return nil
}

func (s *screen) close() {
	if s.id != 0 {
		_, _ = s.notifier.CloseNotification(s.id)
	}
	s.id, s.uuid = 0, ""
}

func (s *screen) render(state *protobuf.State) error {
	g := pending(state)
	if g == nil {
		if pin := state.GetFetcher().GetNiks3Status(); pin != nil && pin.ManualDownload && pin.StorePath != "" &&
			pin.FetchErrorMsg == "" && !state.GetBuilder().GetIsBuilding().GetValue() &&
			!state.GetDeployer().GetIsDeploying().GetValue() {
			current, _ := os.Readlink("/run/current-system")
			if pin.StorePath != current {
				if pin.StorePath == s.available {
					if s.uuid != "" {
						s.close()
					}
					return nil
				}
				s.close()
				body := s.text("Update available. Open CityScanner Maintenance to download it when convenient.",
					"Доступно обновление. Откройте «Обслуживание CityScanner», чтобы скачать его в удобное время.")
				id, err := s.notifier.SendNotification(notify.Notification{
					AppName: "comin", AppIcon: "system-software-update", Summary: s.title,
					Body:          html.EscapeString(body + "\n" + filepath.Base(pin.StorePath)),
					ExpireTimeout: notify.ExpireTimeoutSetByNotificationServer,
				})
				if err == nil {
					s.id, s.available = id, pin.StorePath
				}
				return err
			}
		}
		s.close()
		d := state.GetDeployer().GetDeployment()
		if d == nil {
			return nil
		}
		key := fmt.Sprintf("%s:%s:%t", d.Uuid, d.Status, state.GetNeedToReboot().GetValue())
		if key == s.lastResult {
			return nil
		}
		var body string
		switch d.Status {
		case "running":
			body = s.text("Installing the update.", "Устанавливаем обновление.")
		case "done":
			body = s.text("Update installed.", "Обновление установлено.")
		case "failed":
			body = s.text("Installation failed. Contact your administrator.", "Ошибка установки. Обратитесь к администратору.")
		}
		if body == "" {
			return nil
		}
		if state.GetNeedToReboot().GetValue() {
			body += s.text(" Restart required.", " Требуется перезагрузка.")
		}
		_, err := s.notifier.SendNotification(notify.Notification{AppName: "comin", Summary: s.title, Body: body, ExpireTimeout: notify.ExpireTimeoutSetByNotificationServer})
		if err == nil {
			s.lastResult = key
		}
		return err
	}
	if g.Uuid == s.uuid || g.Uuid == s.dismissed {
		return nil
	}
	s.close()
	release := filepath.Base(g.OutPath)
	if git := g.Source.GetGit(); git != nil {
		release = strings.Split(strings.TrimSpace(git.SelectedCommitMsg), "\n")[0]
	}
	n := notify.Notification{
		AppName: "comin", AppIcon: "system-software-update", Summary: s.title,
		Body: html.EscapeString(s.text("Update downloaded. Install when your work is finished.", "Обновление скачано. Установите его, когда закончите работу.") + "\n" + release),
		Actions: []notify.Action{
			{Key: "install:" + g.Uuid, Label: s.text("Install", "Установить")},
			{Key: "later:" + g.Uuid, Label: s.text("Later", "Позже")},
		},
		Hints:         map[string]dbus.Variant{"resident": dbus.MakeVariant(true)},
		ExpireTimeout: notify.ExpireTimeoutNever,
	}
	id, err := s.notifier.SendNotification(n)
	if err != nil {
		return err
	}
	s.id, s.uuid = id, g.Uuid
	return nil
}

func (s *screen) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := s.api.GetManagerStateContext(ctx)
	if err != nil {
		s.close()
		return err
	}
	return s.render(state)
}

func (s *screen) action(ctx context.Context, action *notify.ActionInvokedSignal) error {
	if action.ID != s.id || s.uuid == "" {
		return nil
	}
	uuid := s.uuid
	if action.ActionKey == "later:"+uuid {
		s.dismissed = uuid
		s.close()
		return nil // The downloaded system and agent proposal stay intact.
	}
	if action.ActionKey != "install:"+uuid {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := s.api.GetManagerStateContext(ctx)
	if err != nil {
		s.close()
		return err
	}
	g := pending(state)
	if g == nil || g.Uuid != uuid {
		return s.render(state)
	}
	// The server checks this exact UUID again at the confirmation boundary.
	if err := s.api.ConfirmContext(ctx, uuid, "deploy"); err != nil {
		return err
	}
	s.close()
	return nil
}

func Run(ctx context.Context, c client.Client, title string) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	caps, err := notify.GetCapabilities(conn)
	if err != nil {
		return err
	}
	if !slices.Contains(caps, "actions") {
		return fmt.Errorf("notification daemon must support actions")
	}
	type notificationEvent struct {
		action *notify.ActionInvokedSignal
		closed *notify.NotificationClosedSignal
	}
	signals := make(chan notificationEvent, 32)
	n, err := notify.New(conn,
		notify.WithOnAction(func(a *notify.ActionInvokedSignal) {
			select {
			case signals <- notificationEvent{action: a}:
			case <-ctx.Done():
			}
		}),
		notify.WithOnClosed(func(a *notify.NotificationClosedSignal) {
			select {
			case signals <- notificationEvent{closed: a}:
			case <-ctx.Done():
			}
		}),
	)
	if err != nil {
		return err
	}
	defer n.Close()
	s := &screen{api: c, notifier: n, title: title, russian: strings.HasPrefix(os.Getenv("LANG"), "ru")}
	defer s.close()
	// A desktop daemon restart invalidates notification IDs. Recover from
	// the current agent state instead of waiting for the next deployment.
	if err := conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, "org.freedesktop.Notifications")); err != nil {
		return err
	}
	owners := make(chan *dbus.Signal, 8)
	conn.Signal(owners)
	defer conn.RemoveSignal(owners)
	events := c.Stream(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			if e.FailureMsg != "" {
				s.close()
				continue
			}
			if snapshot := e.Event.GetManagerState(); snapshot != nil {
				err = s.render(snapshot.State)
			} else if e.Event.GetLog() == nil {
				err = s.refresh(ctx)
			}
		case event := <-signals:
			if event.action != nil {
				err = s.action(ctx, event.action)
			}
			if close := event.closed; close != nil && close.ID == s.id && close.Reason == 2 {
				s.dismissed = s.uuid
				s.id, s.uuid = 0, ""
			}
		case signal := <-owners:
			if signal.Name == "org.freedesktop.DBus.NameOwnerChanged" {
				s.id, s.uuid = 0, ""
				s.available = ""
				err = s.refresh(ctx)
			}
		case <-ticker.C:
			err = s.refresh(ctx)
		}
		if err != nil {
			logrus.Warnf("desktop: %s", err)
		}
	}
}
