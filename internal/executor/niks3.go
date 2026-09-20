package executor

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/nlewo/comin/internal/types"
	"github.com/nlewo/comin/internal/utils"
	"github.com/nlewo/comin/pkg/protobuf"
)

type Niks3 struct{ root string }

func NewNiks3(stateDir string) *Niks3 {
	return &Niks3{root: filepath.Join(stateDir, "gcroots", "niks3-download")}
}

func (n *Niks3) ReadMachineId() (string, error)        { return utils.ReadMachineIdLinux() }
func (n *Niks3) IsStorePathExist(p string) bool        { return isStorePathExist(p) }
func (n *Niks3) NeedToReboot(p, operation string) bool { return utils.NeedToRebootLinux(p, operation) }

// Comin's preparation stage consumes metadata only; it runs no Nix evaluation.
func (n *Niks3) Eval(ctx context.Context, source *protobuf.Source, stdout, stderr io.WriteCloser) (string, string, string, error) {
	s := source.GetNiks3()
	if s == nil {
		return "", "", "", fmt.Errorf("expected a niks3 source")
	}
	if err := types.ValidateNiks3Path(s.StorePath); err != nil {
		return "", "", "", err
	}
	return "", s.StorePath, "", nil
}

func (n *Niks3) Build(ctx context.Context, drvPath, outPath string, stdout, stderr io.WriteCloser) error {
	if err := types.ValidateNiks3Path(outPath); err != nil {
		return err
	}
	// Register a root in the realization command to close the race with GC
	// before the store records last-built-generation. Keep the old ready
	// generation rooted until a replacement has finished downloading.
	return runNixCommand(ctx, "nix-store", []string{
		"--realise", outPath, "--add-root", n.root, "--indirect",
		"--option", "max-jobs", "0", "--option", "builders", "", "--option", "fallback", "false",
	}, stdout, stderr)
}

func (n *Niks3) Deploy(ctx context.Context, outPath, operation string, profiles []string, stdout, stderr io.WriteCloser) (bool, string, error) {
	return deployLinux(ctx, outPath, operation, profiles, stdout, stderr)
}
