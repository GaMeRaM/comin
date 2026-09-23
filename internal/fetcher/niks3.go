package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/jdx/go-netrc"
	"github.com/nlewo/comin/internal/broker"
	"github.com/nlewo/comin/internal/types"
	"github.com/nlewo/comin/pkg/protobuf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Niks3Fetcher follows main and an optional testing channel. pins/<name> contains a
// plain store path, not the JSON returned by the authenticated management API.
type Niks3Fetcher struct {
	config           types.Niks3Fetcher
	broker           *broker.Broker
	client           *http.Client
	trigger          chan struct{}
	prepareRequested atomic.Bool
	isFetching       atomic.Bool
	mu               sync.RWMutex
	state            *protobuf.Niks3Status
}

var errPinMissing = errors.New("pin does not exist")

// Select only a consistent main/testing pair. A missing test means main;
// authentication, transport and malformed-body errors preserve the last proposal.
func (f *Niks3Fetcher) selectPin(ctx context.Context) (string, string, bool, error) {
	main, err := f.fetch(ctx)
	if err != nil || f.config.TestingURL == "" {
		return main, main, false, err
	}
	testingConfig, err := f.config.TestingPin(main)
	if err != nil {
		return "", "", false, err
	}
	testingFetcher := NewNiks3Fetcher(testingConfig, nil)
	testingFetcher.client = f.client
	testing, err := testingFetcher.fetch(ctx)
	if err != nil && !errors.Is(err, errPinMissing) {
		return "", "", false, err
	}
	latest, err := f.fetch(ctx)
	if err != nil {
		return "", "", false, err
	}
	if latest != main {
		return "", "", false, fmt.Errorf("main changed while reading testing pin; retrying next poll")
	}
	if testing != "" && testing != main {
		return testing, main, true, nil
	}
	return main, main, false, nil
}

func NewNiks3Fetcher(config types.Niks3Fetcher, b *broker.Broker) *Niks3Fetcher {
	return &Niks3Fetcher{
		config: config, broker: b,
		client: &http.Client{
			Timeout: time.Duration(config.Timeout) * time.Second,
			// Pins are authoritative channel assignments, not arbitrary links.
			// Do not forward credentials or trust a redirect to another origin.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 || req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("pin redirect must remain on its original origin")
				}
				return nil
			},
		},
		trigger: make(chan struct{}, 1),
		state:   &protobuf.Niks3Status{PinUrl: config.URL, ManualDownload: config.ManualDownload},
	}
}

func (f *Niks3Fetcher) IsFetching() bool { return f.isFetching.Load() }

func (f *Niks3Fetcher) TriggerFetch(_ []string) {
	f.prepareRequested.Store(true)
	f.enqueue()
}

func (f *Niks3Fetcher) TriggerCheck(_ []string) {
	if !f.config.ManualDownload {
		f.TriggerFetch(nil)
		return
	}
	f.enqueue()
}

func (f *Niks3Fetcher) enqueue() {
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

// ReadNiks3Pin is also used by the installer; no running agent is required.
func ReadNiks3Pin(ctx context.Context, config types.Niks3Fetcher) (string, error) {
	return NewNiks3Fetcher(config, nil).fetch(ctx)
}

func (f *Niks3Fetcher) fetch(ctx context.Context) (string, error) {
	u, err := f.config.ParseURL()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(f.config.Timeout)*time.Second)
	defer cancel()
	var reader io.ReadCloser
	if u.Scheme == "s3" {
		reader, err = f.fetchS3(ctx, u)
	} else {
		reader, err = f.fetchHTTP(ctx)
	}
	if err != nil {
		return "", err
	}
	defer reader.Close()
	const limit = 4096
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
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

func (f *Niks3Fetcher) fetchS3(ctx context.Context, u *url.URL) (io.ReadCloser, error) {
	q := u.Query()
	profile := q.Get("profile")
	if profile == "" {
		profile = "default"
	}
	region := q.Get("region")
	if region == "" {
		region = "us-east-1"
	}
	// A fresh provider on each poll observes credential rotation. Explicit file
	// selection prevents falling back to a more privileged ambient AWS identity.
	shared, err := awsconfig.LoadSharedConfigProfile(ctx, profile, func(o *awsconfig.LoadSharedConfigOptions) {
		o.CredentialsFiles = []string{f.config.AWSCredentialsFile}
		o.ConfigFiles = []string{}
	})
	value := shared.Credentials
	if err != nil || value.AccessKeyID == "" || value.SecretAccessKey == "" {
		return nil, fmt.Errorf("cannot read S3 pin credentials for the selected profile")
	}
	client := s3.NewFromConfig(aws.Config{
		Region: region, HTTPClient: f.client,
		Credentials: credentials.NewStaticCredentialsProvider(value.AccessKeyID, value.SecretAccessKey, value.SessionToken),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://" + q.Get("endpoint"))
		o.UsePathStyle = true
	})
	object, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.Host), Key: aws.String(strings.TrimPrefix(u.Path, "/")),
	})
	if err != nil {
		var apiError smithy.APIError
		if errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchKey" {
			return nil, errPinMissing
		}
		return nil, err
	}
	return object.Body, nil
}

func (f *Niks3Fetcher) fetchHTTP(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.config.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	if f.config.NetrcFile != "" {
		if req.URL.Scheme != "https" {
			return nil, fmt.Errorf("pin credentials require HTTPS")
		}
		// Read on every poll so rotating the shared Nix/Comin read credential
		// does not require restarting the agent. Never log file contents.
		data, err := os.ReadFile(f.config.NetrcFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read pin netrc file")
		}
		n, err := netrc.ParseString(string(data))
		if err != nil {
			return nil, fmt.Errorf("cannot parse pin netrc file")
		}
		m := n.Machine(req.URL.Hostname())
		if m == nil || m.Get("login") == "" || m.Get("password") == "" {
			return nil, fmt.Errorf("pin netrc file has no login/password for the pin host")
		}
		req.SetBasicAuth(m.Get("login"), m.Get("password"))
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errPinMissing
		}
		return nil, fmt.Errorf("pin request: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (f *Niks3Fetcher) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.trigger:
				prepare := f.prepareRequested.Swap(false)
				f.isFetching.Store(true)
				outPath, mainPath, testing, err := f.selectPin(ctx)
				f.mu.Lock()
				f.state.FetchedAt = timestamppb.Now()
				f.state.FetchErrorMsg = ""
				if err != nil {
					f.state.FetchErrorMsg = err.Error()
				} else {
					f.state.StorePath = outPath
					f.state.MainStorePath = mainPath
					f.state.IsTesting = testing
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
						Type: &protobuf.Event_Fetched_Niks3Status{Niks3Status: snapshot}, Updated: err == nil, Prepare: prepare && err == nil,
					}}, CreatedAt: timestamppb.Now(),
				})
			}
		}
	}()
}
