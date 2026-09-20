package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/types"
	"github.com/nlewo/comin/pkg/protobuf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Niks3Fetcher follows one channel. The S3 object pins/<name> contains a
// plain store path, not the JSON returned by the authenticated management API.
type Niks3Fetcher struct {
	config     types.Niks3Fetcher
	broker     *broker.Broker
	client     *http.Client
	trigger    chan struct{}
	isFetching atomic.Bool
	mu         sync.RWMutex
	state      *protobuf.Niks3Status
}

func NewNiks3Fetcher(config types.Niks3Fetcher, b *broker.Broker) *Niks3Fetcher {
	return &Niks3Fetcher{
		config: config, broker: b,
		client:  &http.Client{Timeout: time.Duration(config.Timeout) * time.Second},
		trigger: make(chan struct{}, 1),
		state:   &protobuf.Niks3Status{PinUrl: config.URL},
	}
}

func (f *Niks3Fetcher) IsFetching() bool { return f.isFetching.Load() }

func (f *Niks3Fetcher) TriggerFetch(_ []string) {
	// Coalesce concurrent timer/RPC requests, without blocking the API.
	select {
	case f.trigger <- struct{}{}:
	default:
	}
}

func (f *Niks3Fetcher) GetState() *protobuf.Fetcher {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return &protobuf.Fetcher{
		IsFetching: wrapperspb.Bool(f.IsFetching()),
		Status:     &protobuf.Fetcher_Niks3Status{Niks3Status: proto.CloneOf(f.state)},
	}
}

func (f *Niks3Fetcher) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.config.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pin request: HTTP %d", resp.StatusCode)
	}
	const limit = 4096
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	if len(body) > limit {
		return "", fmt.Errorf("pin exceeds %d bytes", limit)
	}
	outPath := strings.TrimSpace(string(body))
	if err := types.ValidateNiks3Path(outPath); err != nil {
		return "", err
	}
	return outPath, nil
}

func (f *Niks3Fetcher) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.trigger:
				f.isFetching.Store(true)
				outPath, err := f.fetch(ctx)
				f.mu.Lock()
				f.state.FetchedAt = timestamppb.Now()
				f.state.FetchErrorMsg = ""
				if err != nil {
					f.state.FetchErrorMsg = err.Error()
				} else {
					f.state.StorePath = outPath
				}
				snapshot := proto.CloneOf(f.state)
				f.mu.Unlock()
				f.isFetching.Store(false)
				// Reconcile every successful read. The manager skips an active,
				// ready or consumed generation, but retries failed downloads.
				// Verified is reserved for Git signature verification. Here Nix
				// verifies cache signatures when realizing the store path.
				f.broker.Publish(&protobuf.Event{
					Type: &protobuf.Event_Fetched_{Fetched: &protobuf.Event_Fetched{
						Type: &protobuf.Event_Fetched_Niks3Status{Niks3Status: snapshot}, Updated: err == nil,
					}}, CreatedAt: timestamppb.Now(),
				})
			}
		}
	}()
}
