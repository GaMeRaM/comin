package manager

import (
	"strings"
	"testing"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/builder"
	"github.com/nlewo/comin/internal/deployer"
	"github.com/nlewo/comin/internal/store"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/stretchr/testify/assert"
)

type pendingExecutor struct {
	ExecutorMock
	exists bool
}

func (e pendingExecutor) IsStorePathExist(string) bool { return e.exists }

func TestRestorePendingManualDeployment(t *testing.T) {
	for _, scenario := range []string{"niks3 ready", "niks3 changed channel", "niks3 changed hostname", "ready", "missing artifact", "different machine", "changed operation", "auto mode", "already attempted"} {
		t.Run(scenario, func(t *testing.T) {
			bk := broker.New()
			bk.Start()
			dir := t.TempDir()
			s, err := store.New(bk, dir+"/state.json", dir+"/gcroots", 2, 2, 5)
			assert.NoError(t, err)
			g := s.NewGeneration("device", ".", "", &protobuf.GitRepositoryStatus{
				SelectedRemoteName: "origin", SelectedBranchName: "main", SelectedCommitId: "release-b",
			})
			if strings.HasPrefix(scenario, "niks3") {
				g = s.NewNiks3Generation("device", "https://cache/pins/device", "/nix/store/release-b", false, "/nix/store/release-b")
			}
			assert.NoError(t, s.GenerationEvalFinished(g.Uuid, "drv", "/nix/store/release-b", "device-id", nil))
			assert.NoError(t, s.GenerationBuildStart(g.Uuid, "test"))
			assert.NoError(t, s.GenerationBuildFinished(g.Uuid, nil))
			assert.NoError(t, s.SetPendingDeployment(g.Uuid, "switch"))
			if scenario == "already attempted" {
				built, err := s.GenerationGet(g.Uuid)
				assert.NoError(t, err)
				s.NewDeployment(&built, "switch", "confirmed", "", "")
			}
			// A genuinely new Store/Confirmer, as after a process restart.
			s, err = store.New(bk, dir+"/state.json", dir+"/gcroots", 2, 2, 5)
			assert.NoError(t, err)
			assert.NoError(t, s.Load())
			e := pendingExecutor{exists: scenario != "missing artifact"}
			mode := Manual
			if scenario == "auto mode" {
				mode = Auto
			}
			c := NewConfirmer(bk, mode, time.Hour, "deploy")
			c.Start()
			m := &Manager{
				machineId: "device-id", storage: s, executor: e,
				Builder:  builder.New(s, e, bk, "repo", ".", "", "device", false, time.Second, time.Second),
				deployer: deployer.New(s, nil, nil, "", bk), DeployConfirmer: c,
				configurationOperations: ConfigurationOperations{"origin": {"main": "switch"}},
			}
			if strings.HasPrefix(scenario, "niks3") {
				m.configurationOperations = ConfigurationOperations{"https://cache/pins/device": {"": "switch"}}
			}
			if scenario == "niks3 changed channel" {
				m.configurationOperations = ConfigurationOperations{"https://cache/pins/other": {"": "switch"}}
			}
			if scenario == "niks3 changed hostname" {
				m.Builder = builder.New(s, e, bk, "", "", "", "other", false, time.Second, time.Second)
			}
			if scenario == "different machine" {
				m.machineId = "other-device"
			}
			if scenario == "changed operation" {
				m.configurationOperations["origin"]["main"] = "boot"
			}
			m.restorePendingDeployment()
			if scenario == "ready" || scenario == "niks3 ready" {
				assert.Equal(t, g.Uuid, c.status().Submitted)
				assert.Empty(t, c.status().Confirmed)
				assert.Nil(t, m.deployer.State().GenerationToDeploy)
			} else {
				assert.Empty(t, c.status().Submitted)
				pending, _, err := s.PendingDeployment()
				assert.NoError(t, err)
				assert.Nil(t, pending)
			}
		})
	}
}
