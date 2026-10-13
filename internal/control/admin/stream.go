package admin

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	pb "github.com/SShogun/GateHouse/gen/go/gatehouse/v1"
	"github.com/SShogun/GateHouse/internal/config"
	"github.com/SShogun/GateHouse/internal/control/revision"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const configPollInterval = time.Second

type streamReceive struct {
	message *pb.DataPlaneServiceConnectRequest
	err     error
}

type configStreamState struct {
	lastSentRevision int64
	lastSentHash     string
	pendingRevision  int64
	pendingHash      string
}

// Connect registers one data-plane process, sends the latest complete
// snapshot, and tracks ACK/NACK/heartbeat state until the stream closes.
func (s *Service) Connect(stream grpc.BidiStreamingServer[pb.DataPlaneServiceConnectRequest, pb.DataPlaneServiceConnectResponse]) error {
	if s.instances == nil {
		return status.Error(codes.Unavailable, "instance status storage is unavailable")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	registration := first.GetRegistration()
	if registration == nil || strings.TrimSpace(first.GetInstanceId()) == "" || strings.TrimSpace(first.GetBootId()) == "" {
		return status.Error(codes.InvalidArgument, "first stream message must register an instance and boot ID")
	}
	if len(first.GetInstanceId()) > maxTextBytes || len(first.GetBootId()) > maxTextBytes || len(registration.GetBuild()) > maxTextBytes {
		return status.Error(codes.InvalidArgument, "instance identity and build must be at most 1024 bytes")
	}
	if registration.GetActiveRevision() < 0 || (registration.GetActiveRevision() == 0 && registration.GetActiveHash() != "") || (registration.GetActiveRevision() > 0 && !validSHA256(registration.GetActiveHash())) {
		return status.Error(codes.InvalidArgument, "registration has invalid active revision state")
	}
	instance := revision.Instance{
		ID: first.GetInstanceId(), BootID: first.GetBootId(), Build: registration.GetBuild(), Connected: true,
		ActiveRevision: registration.GetActiveRevision(), ActiveHash: registration.GetActiveHash(), LastHeartbeat: time.Now(),
	}
	if err := validateReportedRevision(stream.Context(), s.store, instance.ActiveRevision, instance.ActiveHash); err != nil {
		return err
	}
	session, err := s.registerInstance(instance)
	if err != nil {
		return apiError(err)
	}
	subscriptionID, publications := s.subscribePublications()
	defer s.unsubscribePublications(subscriptionID)
	lastNack := ""
	state := configStreamState{}
	defer s.disconnectInstance(session, &instance, &lastNack)

	if err := sendLatestConfig(stream, s.store, &state); err != nil {
		return err
	}
	received := make(chan streamReceive, 1)
	go func() {
		for {
			message, recvErr := stream.Recv()
			select {
			case received <- streamReceive{message: message, err: recvErr}:
			case <-stream.Context().Done():
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()
	poll := time.NewTicker(configPollInterval)
	defer poll.Stop()
	for {
		select {
		case <-session.done:
			return status.Error(codes.Aborted, "a newer stream replaced this instance session")
		case <-stream.Context().Done():
			return stream.Context().Err()
		case result := <-received:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
			if err := applyClientEvent(stream.Context(), s.store, result.message, &instance, &lastNack, &state); err != nil {
				return err
			}
			instance.Connected = true
			instance.LastHeartbeat = time.Now()
			instance.LastNACK = lastNack
			updated, err := s.updateInstance(stream.Context(), session, instance)
			if err != nil {
				return apiError(err)
			}
			if !updated {
				return status.Error(codes.Aborted, "a newer stream replaced this instance session")
			}
			if err := sendLatestConfig(stream, s.store, &state); err != nil {
				return err
			}
		case <-publications:
			if err := sendLatestConfig(stream, s.store, &state); err != nil {
				return err
			}
		case <-poll.C:
			if err := sendLatestConfig(stream, s.store, &state); err != nil {
				return err
			}
		}
	}
}

func (s *Service) registerInstance(instance revision.Instance) (*dataPlaneSession, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session := &dataPlaneSession{done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.instances.UpsertInstance(ctx, instance); err != nil {
		return nil, err
	}
	if previous := s.sessions[instance.ID]; previous != nil {
		close(previous.done)
	}
	s.sessions[instance.ID] = session
	return session, nil
}

func (s *Service) updateInstance(ctx context.Context, session *dataPlaneSession, instance revision.Instance) (bool, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessions[instance.ID] != session {
		return false, nil
	}
	return true, s.instances.UpsertInstance(ctx, instance)
}

func (s *Service) disconnectInstance(session *dataPlaneSession, instance *revision.Instance, lastNack *string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessions[instance.ID] != session {
		return
	}
	delete(s.sessions, instance.ID)
	close(session.done)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	instance.Connected = false
	instance.LastNACK = *lastNack
	instance.LastHeartbeat = time.Now()
	_ = s.instances.UpsertInstance(ctx, *instance)
}

func applyClientEvent(ctx context.Context, store revision.Store, message *pb.DataPlaneServiceConnectRequest, instance *revision.Instance, lastNack *string, state *configStreamState) error {
	if message == nil || message.GetInstanceId() != instance.ID || message.GetBootId() != instance.BootID {
		return status.Error(codes.InvalidArgument, "stream identity changed after registration")
	}
	switch event := message.GetEvent().(type) {
	case *pb.DataPlaneServiceConnectRequest_Ack:
		if event.Ack == nil || event.Ack.Revision <= 0 || !validSHA256(event.Ack.ContentHash) {
			return status.Error(codes.InvalidArgument, "ACK must include a positive revision and SHA-256 hash")
		}
		if err := validatePendingRevision(event.Ack.Revision, event.Ack.ContentHash, state); err != nil {
			return err
		}
		if err := validateReportedRevision(ctx, store, event.Ack.Revision, event.Ack.ContentHash); err != nil {
			return err
		}
		instance.ActiveRevision, instance.ActiveHash = event.Ack.Revision, event.Ack.ContentHash
		*lastNack = ""
		state.pendingRevision, state.pendingHash = 0, ""
	case *pb.DataPlaneServiceConnectRequest_Nack:
		if event.Nack == nil || event.Nack.Revision <= 0 || !validSHA256(event.Nack.ContentHash) || len(event.Nack.Reason) > maxTextBytes || event.Nack.ActiveRevision < 0 || (event.Nack.ActiveRevision == 0 && event.Nack.ActiveHash != "") || (event.Nack.ActiveRevision > 0 && !validSHA256(event.Nack.ActiveHash)) {
			return status.Error(codes.InvalidArgument, "NACK must include a positive revision, SHA-256 hash, and bounded reason")
		}
		if err := validatePendingRevision(event.Nack.Revision, event.Nack.ContentHash, state); err != nil {
			return err
		}
		if event.Nack.ActiveRevision != instance.ActiveRevision || event.Nack.ActiveHash != instance.ActiveHash {
			return status.Error(codes.InvalidArgument, "NACK active revision does not match the instance's current state")
		}
		if err := validateReportedRevision(ctx, store, event.Nack.Revision, event.Nack.ContentHash); err != nil {
			return err
		}
		if err := validateReportedRevision(ctx, store, event.Nack.ActiveRevision, event.Nack.ActiveHash); err != nil {
			return err
		}
		instance.ActiveRevision, instance.ActiveHash = event.Nack.ActiveRevision, event.Nack.ActiveHash
		*lastNack = event.Nack.Reason
		state.pendingRevision, state.pendingHash = 0, ""
	case *pb.DataPlaneServiceConnectRequest_Heartbeat:
		if event.Heartbeat == nil || event.Heartbeat.ActiveRevision < 0 || (event.Heartbeat.ActiveRevision == 0 && event.Heartbeat.ActiveHash != "") || (event.Heartbeat.ActiveRevision > 0 && !validSHA256(event.Heartbeat.ActiveHash)) {
			return status.Error(codes.InvalidArgument, "heartbeat has invalid active revision state")
		}
		if event.Heartbeat.ActiveRevision != instance.ActiveRevision || event.Heartbeat.ActiveHash != instance.ActiveHash {
			return status.Error(codes.InvalidArgument, "heartbeat active revision does not match the instance's current state")
		}
		if err := validateReportedRevision(ctx, store, event.Heartbeat.ActiveRevision, event.Heartbeat.ActiveHash); err != nil {
			return err
		}
		instance.ActiveRevision, instance.ActiveHash = event.Heartbeat.ActiveRevision, event.Heartbeat.ActiveHash
	default:
		return status.Error(codes.InvalidArgument, "stream message must contain ACK, NACK, or heartbeat")
	}
	return nil
}

func validatePendingRevision(number int64, hash string, state *configStreamState) error {
	if number != state.pendingRevision || hash != state.pendingHash || number <= 0 {
		return status.Error(codes.InvalidArgument, "reported revision was not sent on this stream")
	}
	return nil
}

func validateReportedRevision(ctx context.Context, store revision.Store, number int64, hash string) error {
	if number == 0 {
		if hash != "" {
			return status.Error(codes.InvalidArgument, "revision zero cannot have a content hash")
		}
		return nil
	}
	if number < 0 || !validSHA256(hash) {
		return status.Error(codes.InvalidArgument, "reported revision is invalid")
	}
	record, err := store.Get(ctx, number)
	if errors.Is(err, revision.ErrNotFound) {
		return status.Error(codes.InvalidArgument, "reported revision does not exist")
	}
	if err != nil {
		return apiError(err)
	}
	if record.Hash != hash {
		return status.Error(codes.InvalidArgument, "reported revision hash does not match stored content")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sendLatestConfig(stream grpc.BidiStreamingServer[pb.DataPlaneServiceConnectRequest, pb.DataPlaneServiceConnectResponse], store revision.Store, state *configStreamState) error {
	if state.pendingRevision != 0 {
		return nil
	}
	active, err := store.Active(stream.Context())
	if errors.Is(err, revision.ErrNotFound) {
		return nil
	}
	if err != nil {
		return apiError(err)
	}
	if active.Config == nil || active.Number <= 0 || len(active.Hash) != 64 {
		return status.Error(codes.DataLoss, "active revision is incomplete")
	}
	_, actualHash, err := config.EncodeCandidate(active.Config)
	if err != nil || actualHash != active.Hash {
		return status.Error(codes.DataLoss, "active revision hash does not match its configuration")
	}
	if active.Number < state.lastSentRevision {
		return status.Error(codes.DataLoss, "active revision moved behind instance status")
	}
	if active.Number == state.lastSentRevision && active.Hash != state.lastSentHash && state.lastSentRevision != 0 {
		return status.Error(codes.DataLoss, "active revision hash conflicts with instance status")
	}
	if active.Number == state.lastSentRevision && active.Hash == state.lastSentHash {
		return nil
	}
	if err := stream.Send(&pb.DataPlaneServiceConnectResponse{Revision: toProto(active, true)}); err != nil {
		return err
	}
	state.lastSentRevision, state.lastSentHash = active.Number, active.Hash
	state.pendingRevision, state.pendingHash = active.Number, active.Hash
	return nil
}

func (s *Service) subscribePublications() (uint64, <-chan struct{}) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.nextSubscriber++
	id := s.nextSubscriber
	channel := make(chan struct{}, 1)
	s.subscribers[id] = channel
	return id, channel
}

func (s *Service) unsubscribePublications(id uint64) {
	s.notifyMu.Lock()
	delete(s.subscribers, id)
	s.notifyMu.Unlock()
}

func (s *Service) signalPublication() {
	s.notifyMu.Lock()
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	s.notifyMu.Unlock()
}
