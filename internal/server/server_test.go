package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nlewo/comin/internal/broker"
	"github.com/stretchr/testify/require"
)

func TestSocketDirectorySurvivesAgentRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comin", "grpc.sock")
	for range 2 {
		agent := New(broker.New(), nil, path, "0770", "")
		server, listener, err := agent.Init()
		require.NoError(t, err)
		stat, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0770), stat.Mode().Perm())
		server.Stop()
		require.NoError(t, listener.Close())
		stat, err = os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.True(t, stat.IsDir())
	}
}
