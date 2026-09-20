package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/stretchr/testify/assert"
)

func pendingTestStore(t *testing.T) *Store {
	t.Helper()
	bk := broker.New()
	bk.Start()
	t.Cleanup(bk.Stop)
	dir := t.TempDir()
	s, err := New(bk, filepath.Join(dir, "state.json"), filepath.Join(dir, "gcroots"), 2, 2, 5)
	assert.NoError(t, err)
	return s
}

func pendingTestGeneration(t *testing.T, s *Store, commit string) protobuf.Generation {
	t.Helper()
	g := s.NewGeneration("device", ".", "", &protobuf.GitRepositoryStatus{SelectedCommitId: commit})
	assert.NoError(t, s.GenerationEvalFinished(g.Uuid, "drv", "/nix/store/"+commit, "", nil))
	assert.NoError(t, s.GenerationBuildStart(g.Uuid, "test"))
	assert.NoError(t, s.GenerationBuildFinished(g.Uuid, nil))
	g, err := s.GenerationGet(g.Uuid)
	assert.NoError(t, err)
	return g
}

func TestPendingDeploymentSurvivesRestartAndFailedNewBuild(t *testing.T) {
	s := pendingTestStore(t)
	g := pendingTestGeneration(t, s, "release-b")
	assert.NoError(t, s.SetPendingDeployment(g.Uuid, "switch"))
	// A newer failed build must not collect the generation awaiting consent.
	next := s.NewGeneration("device", ".", "", &protobuf.GitRepositoryStatus{SelectedCommitId: "release-c"})
	assert.NoError(t, s.GenerationEvalStarted(next.Uuid))
	assert.NoError(t, s.GenerationEvalFinished(next.Uuid, "drv-c", "/nix/store/c", "", nil))
	assert.NoError(t, s.GenerationBuildStart(next.Uuid, "test"))
	assert.NoError(t, s.GenerationBuildFinished(next.Uuid, errors.New("cache unavailable")))
	s.Commit()

	reloaded, err := New(s.broker, s.filename, filepath.Dir(s.generationGcRoot), 2, 2, 5)
	assert.NoError(t, err)
	assert.NoError(t, reloaded.Load())
	actual, operation, err := reloaded.PendingDeployment()
	assert.NoError(t, err)
	assert.Equal(t, g.Uuid, actual.Uuid)
	assert.Equal(t, g.OutPath, actual.OutPath)
	assert.Equal(t, "release-b", actual.Source.GetGit().SelectedCommitId)
	assert.Equal(t, "switch", operation)
	// RPC snapshots and returned generations cannot mutate persisted state.
	actual.OutPath = "changed"
	reloaded.GetState().PendingDeploymentUuid = "changed"
	actual, _, err = reloaded.PendingDeployment()
	assert.NoError(t, err)
	assert.Equal(t, g.OutPath, actual.OutPath)
}

func TestPendingDeploymentReplacementAndConsumption(t *testing.T) {
	s := pendingTestStore(t)
	first := pendingTestGeneration(t, s, "b")
	assert.NoError(t, s.SetPendingDeployment(first.Uuid, "switch"))
	second := pendingTestGeneration(t, s, "c")
	assert.NoError(t, s.SetPendingDeployment(second.Uuid, "switch"))
	assert.NoError(t, s.ClearPendingDeployment(first.Uuid))
	g, _, err := s.PendingDeployment()
	assert.NoError(t, err)
	assert.Equal(t, second.Uuid, g.Uuid)
	assert.NoError(t, s.ClearPendingDeployment(second.Uuid))
	assert.NoError(t, s.Load())
	g, _, err = s.PendingDeployment()
	assert.NoError(t, err)
	assert.Nil(t, g)
}

func TestPendingDeploymentWriteFailurePreservesSnapshot(t *testing.T) {
	s := pendingTestStore(t)
	g := pendingTestGeneration(t, s, "b")
	s.Commit()
	filename := s.filename
	before, err := os.ReadFile(filename)
	assert.NoError(t, err)
	// A nonexistent parent reliably fails even when tests run as root.
	s.filename = filepath.Join(filepath.Dir(filename), "missing", "state.json")
	assert.Error(t, s.SetPendingDeployment(g.Uuid, "switch"))
	pending, _, err := s.PendingDeployment()
	assert.NoError(t, err)
	assert.Nil(t, pending)
	after, err := os.ReadFile(filename)
	assert.NoError(t, err)
	assert.Equal(t, before, after)
	s.filename = filename
	assert.NoError(t, s.SetPendingDeployment(g.Uuid, "switch"))
}

func TestPendingDeploymentRequiresSuccessfulBuild(t *testing.T) {
	s := pendingTestStore(t)
	g := s.NewGeneration("device", ".", "", &protobuf.GitRepositoryStatus{})
	assert.Error(t, s.SetPendingDeployment(g.Uuid, "switch"))
	assert.Error(t, s.SetPendingDeployment("unknown", "switch"))
}
