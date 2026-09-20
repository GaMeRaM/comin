package client

import (
	"context"
	"fmt"
	"time"

	"github.com/nlewo/comin/pkg/protobuf"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Client struct {
	conn        *grpc.ClientConn
	cominClient protobuf.CominClient
}

type ClientOpts struct {
	UnixSocketPath string
}

func New(clientOpts ClientOpts) (c Client, err error) {
	serverAddr := fmt.Sprintf("unix://%s", clientOpts.UnixSocketPath)
	logrus.Debugf("client: connection to %s", serverAddr)
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	c.conn, err = grpc.NewClient(serverAddr, opts...)
	if err != nil {
		return
	}
	c.cominClient = protobuf.NewCominClient(c.conn)
	return
}
func (c Client) Close() {
	c.conn.Close() // nolint: errcheck
}

func (c Client) GetManagerState() (state *protobuf.State, err error) {
	return c.GetManagerStateContext(context.Background())
}

func (c Client) GetManagerStateContext(ctx context.Context) (*protobuf.State, error) {
	return c.cominClient.GetState(ctx, &emptypb.Empty{})
}

type Streamer struct {
	FailureMsg string
	Event      *protobuf.Event
}

func (c Client) Stream(ctx context.Context) chan Streamer {
	ch := make(chan Streamer)
	go func() {
		defer close(ch)
		send := func(s Streamer) bool {
			select {
			case ch <- s:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for ctx.Err() == nil {
			events, err := c.cominClient.Events(ctx, &emptypb.Empty{})
			if err == nil {
				for {
					var event *protobuf.Event
					event, err = events.Recv()
					if err != nil {
						break
					}
					if !send(Streamer{Event: event}) {
						return
					}
				}
			}
			if ctx.Err() != nil {
				return
			}
			if !send(Streamer{FailureMsg: fmt.Sprintf("event stream: %s", err)}) {
				return
			}
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

func (c Client) Fetch() {
	c.cominClient.Fetch(context.Background(), &emptypb.Empty{}) // nolint: errcheck
}
func (c Client) Suspend() error {
	_, err := c.cominClient.Suspend(context.Background(), &emptypb.Empty{})
	return err
}
func (c Client) Resume() error {
	_, err := c.cominClient.Resume(context.Background(), &emptypb.Empty{})
	return err
}
func (c Client) DeploymentLatestSubmit(operation string) error {
	_, err := c.cominClient.DeploymentLatestSubmit(context.Background(), &protobuf.Operation{OperationSubmitted: operation})
	return err
}

func (c Client) Confirm(generationUUID, for_ string) error {
	return c.ConfirmContext(context.Background(), generationUUID, for_)
}

func (c Client) ConfirmContext(ctx context.Context, generationUUID, for_ string) error {
	_, err := c.cominClient.Confirm(ctx, &protobuf.ConfirmRequest{
		GenerationUuid: generationUUID, For: for_})
	return err
}
