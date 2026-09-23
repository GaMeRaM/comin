package manager

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/builder"
	"github.com/nlewo/comin/internal/deployer"
	"github.com/nlewo/comin/internal/store"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/stretchr/testify/assert"
)

type pinExecutor struct {
	ExecutorMock
	started   chan string
	cancelled chan string
	finish    chan error
}

func (e pinExecutor) Eval(ctx context.Context, source *protobuf.Source, stdout, stderr io.WriteCloser) (string, string, string, error) {
	return "", source.GetNiks3().StorePath, "", nil
}

func (e pinExecutor) Build(ctx context.Context, drv, out string, stdout, stderr io.WriteCloser) error {
	e.started <- out
	select {
	case <-ctx.Done():
		e.cancelled <- out
		return ctx.Err()
	case err := <-e.finish:
		return err
	}
}

func TestNiks3SupersedesDownloadAndRetriesSamePin(t *testing.T) {
	bk := broker.New()
	bk.Start()
	dir := t.TempDir()
	s, err := store.New(bk, dir+"/state.json", dir+"/gcroots", 2, 2, 5)
	if !assert.NoError(t, err) {
		return
	}
	e := pinExecutor{started: make(chan string, 4), cancelled: make(chan string, 4), finish: make(chan error, 4)}
	b := builder.New(s, e, bk, "", "", "", "device", false, time.Second, 5*time.Second)
	bc := NewConfirmer(bk, Without, 0, "build")
	bc.Start()
	dc := NewConfirmer(bk, Manual, 0, "deploy")
	dc.Start()
	m := &Manager{storage: s, Builder: b, executor: e, BuildConfirmer: bc, DeployConfirmer: dc,
		deployer: deployer.New(s, nil, nil, "", bk), brokerEvents: bk.Subscribe(),
		configurationOperations: ConfigurationOperations{"https://cache/pins/device": {"": "switch", "testing": "test"}},
	}
	m.FetchAndBuild(t.Context())
	publishAs := func(path string, testing bool) {
		bk.Publish(&protobuf.Event{Type: &protobuf.Event_Fetched_{Fetched: &protobuf.Event_Fetched{
			Updated: true, Prepare: true, Type: &protobuf.Event_Fetched_Niks3Status{Niks3Status: &protobuf.Niks3Status{
				PinUrl: "https://cache/pins/device", StorePath: path, IsTesting: testing,
			}},
		}}})
	}
	publish := func(path string) { publishAs(path, false) }
	receive := func(ch <-chan string, want string) {
		select {
		case got := <-ch:
			assert.Equal(t, want, got)
		case <-time.After(2 * time.Second):
			t.Fatal("download did not progress")
		}
	}
	bk.Publish(&protobuf.Event{Type: &protobuf.Event_Fetched_{Fetched: &protobuf.Event_Fetched{
		Updated: true, Type: &protobuf.Event_Fetched_Niks3Status{Niks3Status: &protobuf.Niks3Status{
			PinUrl: "https://cache/pins/device", StorePath: "/nix/store/release-b",
		}},
	}}})
	assert.Never(t, func() bool { return b.State().IsBuilding.GetValue() }, 50*time.Millisecond, time.Millisecond,
		"metadata polling must not start a download")
	publish("/nix/store/release-b")
	receive(e.started, "/nix/store/release-b")
	publish("/nix/store/release-c")
	receive(e.cancelled, "/nix/store/release-b")
	receive(e.started, "/nix/store/release-c")
	e.finish <- fmt.Errorf("cache unavailable")
	assert.Eventually(t, func() bool { return b.State().Generation.GetBuildErr() != "" }, time.Second, time.Millisecond)
	failedUUID := b.State().Generation.Uuid
	publish("/nix/store/release-c") // unchanged pin retries a failed transfer
	receive(e.started, "/nix/store/release-c")
	e.finish <- nil
	assert.Eventually(t, func() bool { return dc.status().Submitted != "" }, time.Second, time.Millisecond)
	readyUUID := dc.status().Submitted
	assert.NotEqual(t, failedUUID, readyUUID)
	g, _, err := s.PendingDeployment()
	assert.NoError(t, err)
	if assert.NotNil(t, g) {
		assert.Equal(t, "/nix/store/release-c", g.OutPath)
	}
	assert.Nil(t, m.deployer.State().GenerationToDeploy)
	publish("/nix/store/release-c")
	// A different next pin proves the prior poll was processed, without sleeps.
	publish("/nix/store/release-d")
	receive(e.started, "/nix/store/release-d")
	assert.Equal(t, readyUUID, dc.status().Submitted)
	e.finish <- fmt.Errorf("next release unavailable")
	assert.Eventually(t, func() bool { return b.State().Generation.GetBuildErr() != "" }, time.Second, time.Millisecond)
	g, _, err = s.PendingDeployment()
	assert.NoError(t, err)
	if assert.NotNil(t, g) {
		assert.Equal(t, readyUUID, g.Uuid)
	}
	publishAs("/nix/store/release-t", true)
	receive(e.started, "/nix/store/release-t")
	e.finish <- nil
	assert.Eventually(t, func() bool {
		p, op, _ := s.PendingDeployment()
		return p != nil && p.OutPath == "/nix/store/release-t" && op == "test" && dc.status().Submitted == p.Uuid
	}, time.Second, time.Millisecond)
	testUUID := dc.status().Submitted
	publishAs("/nix/store/release-t", false) // same output, now main: must prepare a switch
	receive(e.started, "/nix/store/release-t")
	assert.Error(t, dc.ConfirmCurrent(testUUID))
	e.finish <- nil
	assert.Eventually(t, func() bool {
		p, op, _ := s.PendingDeployment()
		return p != nil && p.OutPath == "/nix/store/release-t" && p.Uuid != testUUID && op == "switch"
	}, time.Second, time.Millisecond)
}
