package store

import (
	"fmt"

	"github.com/nlewo/comin/pkg/protobuf"
	"google.golang.org/protobuf/proto"
)

// PendingDeployment returns a snapshot, independent of live build state.
func (s *Store) PendingDeployment() (*protobuf.Generation, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persisted.PendingDeploymentUuid == "" {
		return nil, "", nil
	}
	g, err := s.generationGet(s.persisted.PendingDeploymentUuid)
	if err != nil {
		return nil, "", err
	}
	return proto.CloneOf(g), s.persisted.PendingDeploymentOperation, nil
}

// SetPendingDeployment must succeed before publishing a manual confirmation.
func (s *Store) SetPendingDeployment(uuid, operation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.generationGet(uuid)
	if err != nil {
		return err
	}
	if g.BuildStatus != Built.String() || g.BuildErr != "" || g.OutPath == "" {
		return fmt.Errorf("store: generation %s is not ready for deployment", uuid)
	}
	previousUUID, previousOperation := s.persisted.PendingDeploymentUuid, s.persisted.PendingDeploymentOperation
	s.persisted.PendingDeploymentUuid, s.persisted.PendingDeploymentOperation = uuid, operation
	if err := s.commit(); err != nil {
		s.persisted.PendingDeploymentUuid, s.persisted.PendingDeploymentOperation = previousUUID, previousOperation
		return err
	}
	return nil
}

// A completed older deployment must not clear a newer pending release.
func (s *Store) ClearPendingDeployment(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persisted.PendingDeploymentUuid != uuid {
		return nil
	}
	operation := s.persisted.PendingDeploymentOperation
	s.persisted.PendingDeploymentUuid, s.persisted.PendingDeploymentOperation = "", ""
	if err := s.commit(); err != nil {
		s.persisted.PendingDeploymentUuid, s.persisted.PendingDeploymentOperation = uuid, operation
		return err
	}
	return nil
}
