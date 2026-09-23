package fetcher

import (
	"context"
	"github.com/nlewo/comin/pkg/protobuf"
)

// Fetcher discovers a configuration; the executor prepares its closure.
type Fetcher interface {
	Start(context.Context)
	TriggerFetch([]string)
	TriggerCheck([]string)
	IsFetching() bool
	GetState() *protobuf.Fetcher
}
