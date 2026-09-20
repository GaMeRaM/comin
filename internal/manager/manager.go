// The manager is in charge of managing relationship between
// components. Basically, it receives new commits from the fetcher,
// call the builder to evaluate and build them. Finally, it submits
// these builds to the deployer.

package manager

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/builder"
	"github.com/nlewo/comin/internal/deployer"
	"github.com/nlewo/comin/internal/executor"
	"github.com/nlewo/comin/internal/fetcher"
	"github.com/nlewo/comin/internal/prometheus"
	"github.com/nlewo/comin/internal/scheduler"
	"github.com/nlewo/comin/internal/store"
	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type Manager struct {
	// The machine id of the current host. It is used to ensure
	// the optionnal machine-id found at evaluation time
	// corresponds to the machine-id of this host.
	machineId string

	// The hostname of the current host.
	Hostname string

	stateRequestCh chan struct{}
	stateResultCh  chan *protobuf.State

	needToReboot bool

	prometheus      prometheus.Prometheus
	storage         *store.Store
	scheduler       scheduler.Scheduler
	Fetcher         fetcher.Fetcher
	Builder         *builder.Builder
	deployer        *deployer.Deployer
	executor        executor.Executor
	BuildConfirmer  *Confirmer
	DeployConfirmer *Confirmer

	configurationOperations ConfigurationOperations
	// Owned by FetchAndBuild; used to ignore completion of a superseded pin.
	niks3Target string

	isSuspended bool

	broker       *broker.Broker
	brokerEvents chan *protobuf.Event
}

func New(s *store.Store,
	p prometheus.Prometheus,
	sched scheduler.Scheduler,
	fetcher fetcher.Fetcher,
	builder *builder.Builder,
	deployer *deployer.Deployer,
	machineId string,
	hostname string,
	executor executor.Executor,
	buildConfirmer *Confirmer,
	deployConfirmer *Confirmer,
	broker *broker.Broker,
	configurationOperations ConfigurationOperations,
) *Manager {

	m := &Manager{
		machineId: machineId,
		Hostname:  hostname,

		stateRequestCh:          make(chan struct{}),
		stateResultCh:           make(chan *protobuf.State),
		prometheus:              p,
		storage:                 s,
		scheduler:               sched,
		Fetcher:                 fetcher,
		Builder:                 builder,
		deployer:                deployer,
		executor:                executor,
		BuildConfirmer:          buildConfirmer,
		DeployConfirmer:         deployConfirmer,
		broker:                  broker,
		configurationOperations: configurationOperations,
		brokerEvents:            broker.Subscribe(),
	}
	return m
}

func (m *Manager) GetState() *protobuf.State {
	m.stateRequestCh <- struct{}{}
	return <-m.stateResultCh
}

func (m *Manager) toState() *protobuf.State {
	return &protobuf.State{
		NeedToReboot:    wrapperspb.Bool(m.needToReboot),
		IsSuspended:     wrapperspb.Bool(m.isSuspended),
		Builder:         m.Builder.State(),
		Deployer:        m.deployer.State(),
		Fetcher:         m.Fetcher.GetState(),
		Store:           m.storage.GetState(),
		BuildConfirmer:  m.BuildConfirmer.status(),
		DeployConfirmer: m.DeployConfirmer.status(),
	}
}

func (m *Manager) DeploymentLatestSubmit(operation string) error {
	latest := m.storage.GetDeploymentLastest()
	if latest == nil {
		return fmt.Errorf("manager: no previous deployment")
	}
	// If no operation is provided, use default based on branch type
	if operation == "" {
		if latest.Generation.Source.GetNiks3() != nil {
			operation = m.operationForSource(latest.Generation.Source)
		} else if latest.Generation.Source.GetGit().GetSelectedBranchIsTesting().GetValue() {
			operation = "test"
		} else {
			operation = "switch"
		}
	}
	reason := fmt.Sprintf("The latest deployment %s has been resubmitted", latest.Uuid)
	m.deployer.Submit(latest.Generation, operation, true, reason)
	return nil
}

func (m *Manager) Suspend() error {
	if m.isSuspended {
		return fmt.Errorf("the manager is already suspended")
	}
	if err := m.Builder.Suspend(); err != nil {
		return err
	}
	m.deployer.Suspend("manager has been manually suspended")
	m.isSuspended = true
	m.broker.Publish(&protobuf.Event{Type: &protobuf.Event_Suspend_{Suspend: &protobuf.Event_Suspend{}}, CreatedAt: timestamppb.New(time.Now().UTC())})
	return nil
}

func (m *Manager) Resume(ctx context.Context) error {
	if !m.isSuspended {
		return fmt.Errorf("the manager is not suspended")
	}
	if err := m.Builder.Resume(ctx); err != nil {
		return err
	}
	m.deployer.Resume()
	m.isSuspended = false
	m.broker.Publish(&protobuf.Event{Type: &protobuf.Event_Resume_{Resume: &protobuf.Event_Resume{}}, CreatedAt: timestamppb.New(time.Now().UTC())})
	return nil
}

// FetchAndBuild fetches new commits. If a new commit is available, it
// evaluates and builds the derivation. Once built, it pushes the
// generation on a channel which is consumed by the deployer.
func (m *Manager) FetchAndBuild(ctx context.Context) {
	go func() {
		m.restorePendingDeployment()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-m.brokerEvents:
				if fetched := e.GetFetched(); fetched != nil {
					if !fetched.Updated {
						continue
					}
					if pin := fetched.GetNiks3Status(); pin != nil {
						m.prepareNiks3(ctx, pin)
						continue
					}
					rs := fetched.GetGitRepositoryStatus()
					if fetched.Verified {
						logrus.Infof("manager: a generation is evaluating for commit %s", rs.SelectedCommitId)
						generation := m.storage.NewGeneration(m.Builder.GetHostname(), m.Builder.GetRepositoryDir(), m.Builder.GetSystemAttr(), rs)
						err := m.Builder.Eval(ctx, &generation)
						if err != nil {
							logrus.Error(err)
						}
					} else {
						logrus.Infof("manager: the commit %s is not evaluated because it is not signed", rs.SelectedCommitId)
					}
				}
			case generationUUID := <-m.Builder.EvaluationDone:
				generation, err := m.storage.GenerationGet(generationUUID)
				if err != nil {
					logrus.Error(err)
					continue
				}
				if generation.EvalErr != "" || m.superseded(&generation) {
					continue
				}
				if generation.MachineId != "" && m.machineId != generation.MachineId {
					logrus.Infof("manager: the comin.machineId %s is not the host machine-id %s", generation.MachineId, m.machineId)
				} else {
					logrus.Infof("manager: the build of the generation %s is submitted", generation.Uuid)
					m.BuildConfirmer.Submit(generationUUID)
				}
			case generationUUID := <-m.BuildConfirmer.confirmed:
				m.Builder.SubmitBuild(ctx, generationUUID)

			case generationUUID := <-m.Builder.BuildDone:
				generation, err := m.storage.GenerationGet(generationUUID)
				if err != nil {
					logrus.Error(err)
					continue
				}
				if generation.BuildErr == "" {
					if m.superseded(&generation) {
						continue
					}
					logrus.Infof("manager: generation %s is available for deployment", generationUUID)
					operation := m.operationForSource(generation.Source)
					if !m.deployer.IsAlreadyDeployed(&generation, operation) {
						if m.DeployConfirmer.Mode() == Manual {
							if err := m.storage.SetPendingDeployment(generationUUID, operation); err != nil {
								logrus.Errorf("manager: cannot persist pending deployment: %s", err)
								continue
							}
						}
						m.DeployConfirmer.Submit(generationUUID)
					}
				} else {
					logrus.Debugf("manager: the generation %s is not being deployed because it has the error: %s", generationUUID, generation.BuildErr)
				}
			case generationUUID := <-m.DeployConfirmer.confirmed:
				generation, err := m.storage.GenerationGet(generationUUID)
				if err != nil {
					logrus.Error(err)
					continue
				}
				operation := m.operationForSource(generation.Source)
				if m.DeployConfirmer.Mode() == Manual {
					pending, savedOperation, err := m.storage.PendingDeployment()
					if err != nil || pending == nil || pending.Uuid != generationUUID || savedOperation != operation {
						logrus.Errorf("manager: confirmation does not match the persisted pending deployment: %s", generationUUID)
						continue
					}
				}
				reason := fmt.Sprintf("The generation %s needs to be deployed", generationUUID)
				m.deployer.Submit(&generation, operation, false, reason)
			}
		}
	}()
}

// Restore only an unconsumed manual proposal. Neither approval nor an
// interrupted deployment is replayed automatically after a restart.
func (m *Manager) restorePendingDeployment() {
	g, operation, err := m.storage.PendingDeployment()
	if err != nil {
		logrus.Errorf("manager: cannot restore pending deployment: %s", err)
		return
	}
	if g == nil {
		return
	}
	valid := m.DeployConfirmer.Mode() == Manual &&
		g.BuildStatus == store.Built.String() && g.BuildErr == "" && g.OutPath != "" &&
		(g.MachineId == "" || g.MachineId == m.machineId) &&
		m.pendingSourceMatches(g.Source, operation) &&
		m.executor.IsStorePathExist(g.OutPath)
	// A deployment record means this approval was already consumed, even
	// if Comin stopped before recording the final activation result.
	for _, d := range m.storage.GetState().Deployments {
		if d.Generation.GetUuid() == g.Uuid {
			valid = false
		}
	}
	if !valid || m.deployer.IsAlreadyDeployed(g, operation) {
		if err := m.storage.ClearPendingDeployment(g.Uuid); err != nil {
			logrus.Errorf("manager: cannot clear pending deployment: %s", err)
		}
		return
	}
	logrus.Infof("manager: restoring manual confirmation for generation %s", g.Uuid)
	m.DeployConfirmer.Submit(g.Uuid)
}

func (m *Manager) operationForSource(source *protobuf.Source) string {
	if pin := source.GetNiks3(); pin != nil {
		return m.niks3Operation(pin.PinUrl, pin.IsTesting)
	}
	git := source.GetGit()
	return m.getOperationFromConfigurationOperations(git.GetSelectedRemoteName(), git.GetSelectedBranchName())
}

func (m *Manager) pendingSourceMatches(source *protobuf.Source, operation string) bool {
	if operation == "" {
		return false
	}
	if pin := source.GetNiks3(); pin != nil {
		return pin.Hostname == m.Builder.GetHostname() && m.niks3Operation(pin.PinUrl, pin.IsTesting) == operation
	}
	git := source.GetGit()
	return git != nil && git.Hostname == m.Builder.GetHostname() &&
		m.configurationOperations[git.SelectedRemoteName][git.SelectedBranchName] == operation
}

func (m *Manager) superseded(g *protobuf.Generation) bool {
	return g.Source.GetNiks3() != nil && niks3Target(g.Source.GetNiks3().StorePath, g.Source.GetNiks3().IsTesting) != m.niks3Target
}

func niks3Target(path string, testing bool) string { return fmt.Sprintf("%s:%t", path, testing) }

func (m *Manager) niks3Operation(url string, testing bool) string {
	branch := ""
	if testing {
		branch = "testing"
	}
	return m.configurationOperations[url][branch]
}

func (m *Manager) prepareNiks3(ctx context.Context, pin *protobuf.Niks3Status) {
	operation := m.niks3Operation(pin.PinUrl, pin.IsTesting)
	if pin.FetchErrorMsg != "" || operation == "" {
		return
	}
	m.niks3Target = niks3Target(pin.StorePath, pin.IsTesting)
	if pending, _, err := m.storage.PendingDeployment(); err == nil && pending != nil {
		previous := pending.Source.GetNiks3()
		if previous != nil && previous.IsTesting && niks3Target(previous.StorePath, true) != m.niks3Target {
			// A withdrawn/reset test must not remain installable while its
			// replacement downloads. Transport errors never enter this branch.
			m.DeployConfirmer.Withdraw()
			if err := m.storage.ClearPendingDeployment(pending.Uuid); err != nil {
				logrus.Errorf("manager: cannot withdraw testing proposal: %s", err)
				return
			}
		}
	}
	current := m.Builder.State().Generation
	if current != nil && current.Source.GetNiks3().GetStorePath() == pin.StorePath &&
		current.Source.GetNiks3().GetIsTesting() == pin.IsTesting &&
		current.EvalErr == "" && current.BuildErr == "" {
		return // Includes a suspended download; let Resume continue it.
	}
	// A changed pin supersedes an unfinished download. Stop waits for it
	// before another generation can use the same executor and staging root.
	m.Builder.Stop()
	if pending, savedOperation, err := m.storage.PendingDeployment(); err == nil && pending != nil &&
		pending.Source.GetNiks3().GetPinUrl() == pin.PinUrl && pending.OutPath == pin.StorePath && savedOperation == operation {
		return // Preserve the persisted UUID across online restarts too.
	}
	if last := m.deployer.State().Deployment; last != nil && last.Generation.OutPath == pin.StorePath && last.Operation == operation {
		return // An already attempted installation needs explicit resubmission.
	}
	generation := m.storage.NewNiks3Generation(m.Builder.GetHostname(), pin.PinUrl, pin.StorePath, pin.IsTesting, pin.MainStorePath)
	if err := m.Builder.Eval(ctx, &generation); err != nil {
		logrus.Error(err)
	}
}

// ConfigurationOperations is a map describing the operation associated
// to each remote/branch. It is a map looking such as:
// { origin: { main: switch, testing: test }, local { main: switch }
type ConfigurationOperations map[string](map[string]string)

func (m *Manager) getOperationFromConfigurationOperations(remote, branch string) (operation string) {
	operation = "test"
	branches, ok := m.configurationOperations[remote]
	if !ok {
		logrus.Errorf("manager: could not get the remote %s. Assuming 'test' operation", remote)
		return
	}
	operation, ok = branches[branch]
	if !ok {
		logrus.Errorf("manager: could not get the operation for the branch %s/%s. Assuming test operation", remote, branch)
		return
	}
	return
}

func (m *Manager) Run(ctx context.Context) {
	logrus.Infof("manager: starting with machineId=%s", m.machineId)
	lastDpl := m.deployer.State().Deployment
	if lastDpl != nil {
		m.needToReboot = m.executor.NeedToReboot(lastDpl.Generation.OutPath, lastDpl.Operation)
	}

	m.FetchAndBuild(ctx)
	m.deployer.Run(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stateRequestCh:
			m.stateResultCh <- m.toState()
		case dpl := <-m.deployer.DeploymentDoneCh:
			if err := m.storage.ClearPendingDeployment(dpl.Generation.Uuid); err != nil {
				logrus.Errorf("manager: cannot clear completed pending deployment: %s", err)
			}
			m.needToReboot = m.executor.NeedToReboot(dpl.Generation.OutPath, dpl.Operation)
			if m.needToReboot {
				e := &protobuf.Event_RebootRequired{Deployment: dpl}
				m.broker.Publish(&protobuf.Event{Type: &protobuf.Event_RebootRequired_{RebootRequired: e}, CreatedAt: timestamppb.New(time.Now().UTC())})
			}
			if dpl.RestartComin.GetValue() {
				// TODO: stop contexts
				logrus.Infof("manager: comin needs to be restarted")
				logrus.Infof("manager: exiting comin to let the service manager restart it")
				os.Exit(0)
			}
		}
	}
}
