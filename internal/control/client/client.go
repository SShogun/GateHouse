package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultHeartbeatInterval = 5 * time.Second
	defaultReconnectMin      = 250 * time.Millisecond
	defaultReconnectMax      = 10 * time.Second
	maxNACKReasonBytes       = 1024
)

// ControlClientConfig describes one data-plane connection to the loopback
// control-plane stream. The connection retries until its context is canceled.
type ControlClientConfig struct {
	Endpoint          string
	InstanceID        string
	BootID            string
	Build             string
	Manager           *gateway.Manager
	HeartbeatInterval time.Duration
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
	DialOptions       []grpc.DialOption
	Logger            *slog.Logger
}

type ControlClient struct {
	config     ControlClientConfig
	client     pb.DataPlaneServiceClient
	connection *grpc.ClientConn
	logger     *slog.Logger
}

func NewControlClient(config ControlClientConfig) (*ControlClient, error) {
	if config.Manager == nil {
		return nil, errors.New("gateway manager is required")
	}
	if strings.TrimSpace(config.InstanceID) == "" || strings.TrimSpace(config.BootID) == "" {
		return nil, errors.New("instance ID and boot ID are required")
	}
	if len(config.InstanceID) > 1024 || len(config.BootID) > 1024 || len(config.Build) > 1024 {
		return nil, errors.New("instance ID, boot ID, and build must be at most 1024 bytes")
	}
	if err := validateLoopbackControlEndpoint(config.Endpoint); err != nil {
		return nil, err
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.ReconnectMin <= 0 {
		config.ReconnectMin = defaultReconnectMin
	}
	if config.ReconnectMax < config.ReconnectMin {
		config.ReconnectMax = defaultReconnectMax
	}
	options := append([]grpc.DialOption(nil), config.DialOptions...)
	options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()))
	connection, err := grpc.NewClient(config.Endpoint, options...)
	if err != nil {
		return nil, fmt.Errorf("create control-plane client: %w", err)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &ControlClient{config: config, client: pb.NewDataPlaneServiceClient(connection), connection: connection, logger: logger}, nil
}

func validateLoopbackControlEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("control endpoint must be a loopback host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("control endpoint port must be between 1 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("control endpoint must use a literal loopback IP")
	}
	return nil
}

// Run reconnects after control-plane outages while leaving the active gateway
// snapshot installed. It returns nil after a normal context cancellation.
func (c *ControlClient) Run(ctx context.Context) error {
	defer c.Close()
	backoff := c.config.ReconnectMin
	for {
		if ctx.Err() != nil {
			c.config.Manager.SetConnected(false)
			return nil
		}
		err := c.runSession(ctx)
		c.config.Manager.SetConnected(false)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = errors.New("control stream ended")
		}
		c.logger.Warn("control-plane stream disconnected; reconnecting", "endpoint", c.config.Endpoint, "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
		if backoff < c.config.ReconnectMax/2 {
			backoff *= 2
		} else {
			backoff = c.config.ReconnectMax
		}
	}
}

func (c *ControlClient) Close() error { return c.connection.Close() }

func (c *ControlClient) runSession(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stream, err := c.client.Connect(ctx)
	if err != nil {
		return err
	}
	status := c.config.Manager.Status()
	registration := &pb.DataPlaneServiceConnectRequest{
		InstanceId: c.config.InstanceID,
		BootId:     c.config.BootID,
		Event: &pb.DataPlaneServiceConnectRequest_Registration{Registration: &pb.DataPlaneRegistration{
			Build: c.config.Build, ActiveRevision: status.Revision, ActiveHash: status.Hash,
		}},
	}
	if err := stream.Send(registration); err != nil {
		return err
	}
	c.config.Manager.SetConnected(true)
	updates := make(chan *pb.DataPlaneServiceConnectResponse, 1)
	receiveErrors := make(chan error, 1)
	go func() {
		for {
			update, receiveErr := stream.Recv()
			if receiveErr != nil {
				select {
				case receiveErrors <- receiveErr:
				case <-ctx.Done():
				}
				return
			}
			select {
			case updates <- update:
			case <-ctx.Done():
				return
			}
		}
	}()
	heartbeat := time.NewTicker(c.config.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			return err
		case <-heartbeat.C:
			if err := stream.Send(c.heartbeatMessage()); err != nil {
				return err
			}
			c.config.Manager.Heartbeat()
		case update := <-updates:
			if update == nil || update.Revision == nil {
				return errors.New("control plane sent an empty config update")
			}
			revision := update.Revision
			activateErr := c.config.Manager.Activate(revision.Revision, revision.ContentHash, revision.Config)
			message := &pb.DataPlaneServiceConnectRequest{InstanceId: c.config.InstanceID, BootId: c.config.BootID}
			if activateErr != nil {
				active := c.config.Manager.Status()
				message.Event = &pb.DataPlaneServiceConnectRequest_Nack{Nack: &pb.ConfigNack{Revision: revision.Revision, ContentHash: revision.ContentHash, Reason: boundedReason(activateErr.Error()), ActiveRevision: active.Revision, ActiveHash: active.Hash}}
			} else {
				message.Event = &pb.DataPlaneServiceConnectRequest_Ack{Ack: &pb.ConfigAck{Revision: revision.Revision, ContentHash: revision.ContentHash}}
			}
			if err := stream.Send(message); err != nil {
				return err
			}
		}
	}
}

func (c *ControlClient) heartbeatMessage() *pb.DataPlaneServiceConnectRequest {
	status := c.config.Manager.Status()
	return &pb.DataPlaneServiceConnectRequest{
		InstanceId: c.config.InstanceID,
		BootId:     c.config.BootID,
		Event: &pb.DataPlaneServiceConnectRequest_Heartbeat{Heartbeat: &pb.DataPlaneHeartbeat{
			ActiveRevision: status.Revision, ActiveHash: status.Hash,
		}},
	}
}

func boundedReason(reason string) string {
	for len(reason) > maxNACKReasonBytes {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return reason
}
