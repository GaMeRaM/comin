package desktop

import (
	"context"
	"fmt"
	"testing"

	"github.com/esiqveland/notify"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/stretchr/testify/assert"
)

type fakeNotifier struct {
	notes  []notify.Notification
	closed []uint32
}

func (n *fakeNotifier) SendNotification(note notify.Notification) (uint32, error) {
	n.notes = append(n.notes, note)
	return uint32(len(n.notes)), nil
}
func (n *fakeNotifier) CloseNotification(id uint32) (bool, error) {
	n.closed = append(n.closed, id)
	return true, nil
}
func (n *fakeNotifier) Close() error { return nil }

func (n *fakeNotifier) GetCapabilities() ([]string, error) { return []string{"actions"}, nil }
func (n *fakeNotifier) GetServerInformation() (notify.ServerInformation, error) {
	return notify.ServerInformation{}, nil
}

type fakeAgent struct {
	state     *protobuf.State
	err       error
	confirmed []string
}

func (a *fakeAgent) GetManagerStateContext(context.Context) (*protobuf.State, error) {
	return a.state, a.err
}
func (a *fakeAgent) ConfirmContext(_ context.Context, uuid, scope string) error {
	a.confirmed = append(a.confirmed, uuid)
	return nil
}

func readyState(uuid string) *protobuf.State {
	return &protobuf.State{
		DeployConfirmer: &protobuf.Confirmer{Submitted: uuid},
		Store:           &protobuf.Store{Generations: []*protobuf.Generation{{Uuid: uuid, BuildStatus: "built", OutPath: "/nix/store/release-" + uuid}}},
	}
}

func TestAvailableReleaseNotifiesOnceWithoutPreparing(t *testing.T) {
	path := "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-nixos-system-new"
	state := &protobuf.State{Fetcher: &protobuf.Fetcher{Status: &protobuf.Fetcher_Niks3Status{
		Niks3Status: &protobuf.Niks3Status{StorePath: path, ManualDownload: true},
	}}}
	n := &fakeNotifier{}
	s := &screen{notifier: n, title: "CityScanner"}
	assert.NoError(t, s.render(state))
	assert.NoError(t, s.render(state))
	assert.Len(t, n.notes, 1)
	assert.Contains(t, n.notes[0].Body, "Update available")
	assert.Empty(t, n.notes[0].Actions)
}

func TestRestoreAndRefreshPendingNotification(t *testing.T) {
	a := &fakeAgent{state: readyState("b")}
	n := &fakeNotifier{}
	s := &screen{api: a, notifier: n, title: "Comin"}
	assert.NoError(t, s.render(a.state)) // initial ManagerState, without a new event
	assert.Equal(t, "install:b", n.notes[0].Actions[0].Key)
	assert.NoError(t, s.refresh(t.Context()))
	assert.Len(t, n.notes, 1)
	a.err = fmt.Errorf("agent restarting")
	assert.Error(t, s.refresh(t.Context()))
	assert.Zero(t, s.id)
	a.err = nil
	assert.NoError(t, s.refresh(t.Context()))
	assert.Len(t, n.notes, 2)
	assert.NoError(t, s.action(t.Context(), &notify.ActionInvokedSignal{ID: s.id, ActionKey: "install:b"}))
	assert.Equal(t, []string{"b"}, a.confirmed)
}

func TestStaleActionCannotConfirmReplacement(t *testing.T) {
	a := &fakeAgent{state: readyState("b")}
	n := &fakeNotifier{}
	s := &screen{api: a, notifier: n}
	assert.NoError(t, s.render(a.state))
	oldID := s.id
	a.state = readyState("c") // changed on the agent, no desktop event yet
	assert.NoError(t, s.action(t.Context(), &notify.ActionInvokedSignal{ID: oldID, ActionKey: "install:b"}))
	assert.Empty(t, a.confirmed)
	assert.Equal(t, "c", s.uuid)
	// Even a daemon reusing the numeric ID cannot retarget the action key.
	assert.NoError(t, s.action(t.Context(), &notify.ActionInvokedSignal{ID: s.id, ActionKey: "install:b"}))
	assert.Empty(t, a.confirmed)
}

func TestLaterKeepsAgentProposal(t *testing.T) {
	a := &fakeAgent{state: readyState("b")}
	n := &fakeNotifier{}
	s := &screen{api: a, notifier: n}
	assert.NoError(t, s.render(a.state))
	assert.NoError(t, s.action(t.Context(), &notify.ActionInvokedSignal{ID: s.id, ActionKey: "later:b"}))
	assert.NoError(t, s.refresh(t.Context()))
	assert.Len(t, n.notes, 1)
	assert.Equal(t, "b", pending(a.state).Uuid)
	assert.Empty(t, a.confirmed)
	// Reopening notifications restores the button from the same agent state.
	s = &screen{api: a, notifier: n}
	assert.NoError(t, s.refresh(t.Context()))
	assert.Equal(t, "b", s.uuid)
}
