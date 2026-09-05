package session

import (
	"context"
	"time"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/pkg/xcodes"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

// Service handles session management RPCs.
type Service struct {
	db         *gorm.DB
	sessionMgr *Manager
}

// New creates a new session Service.
func New(db *gorm.DB, sessionMgr *Manager) *Service {
	return &Service{
		db:         db,
		sessionMgr: sessionMgr,
	}
}

// RefreshSession extends the current session TTL and updates last_active_at in DB.
// Uses Validate (single Lua round trip) to read data and renew atomically.
func (s *Service) RefreshSession(ctx context.Context, req *pb.RefreshSessionRequest) (*emptypb.Empty, error) {
	sessionID := req.GetSessionId()
	data, err := s.sessionMgr.Validate(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	_ = data // only used to verify the session exists; renewal happened in Validate
	if err := dal.UpdateSessionLastActive(ctx, s.db, sessionID); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// GetSession resolves a session_id to its user_id and metadata for the BFF
// validate-on-use path. Uses Validate, which atomically refreshes the session
// TTL (the sliding-window mechanism: every authenticated request extends the
// session). Read-only callers that must not slide the window should call a
// pure-read path instead.
func (s *Service) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	data, err := s.sessionMgr.Validate(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	ttl, err := s.sessionMgr.RemainingTTL(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &pb.GetSessionResponse{
		UserId:      data.UserID,
		CreatedAt:   timestamppb.New(data.LoginAt),
		Ip:          data.LoginIP,
		UserAgent:   data.UserAgent,
		Os:          data.OS,
		Browser:     data.Browser,
		LoginMethod: data.LoginMethod,
	}
	if ttl > 0 {
		resp.ExpiresAt = timestamppb.New(time.Now().Add(ttl))
	}
	return resp, nil
}

// IssueSessionCode mints a one-time short code referencing the given
// session_id. Used by the OAuth callback service to hand the session back
// to the business side via URL query without leaking session_id into
// referer/logs/history.
func (s *Service) IssueSessionCode(ctx context.Context, req *pb.IssueSessionCodeRequest) (*pb.IssueSessionCodeResponse, error) {
	if req.GetSessionId() == "" {
		return nil, xcodes.ErrBadRequest.New("session_id is required")
	}
	code, err := s.sessionMgr.IssueSessionCode(ctx, req.GetSessionId())
	if err != nil {
		return nil, xcodes.ErrInternal.Wrap(err)
	}
	return &pb.IssueSessionCodeResponse{Code: code}, nil
}

// ExchangeSessionCode trades a one-time short code for the underlying
// session_id + user_id. Not-found / expired / replay all map to
// ErrSessionInvalid — caller cannot distinguish, which is intentional (no
// information leak). Read-only Get (no TTL refresh) — exchange is not a
// validate-on-use path.
func (s *Service) ExchangeSessionCode(ctx context.Context, req *pb.ExchangeSessionCodeRequest) (*pb.ExchangeSessionCodeResponse, error) {
	if req.GetCode() == "" {
		return nil, xcodes.ErrBadRequest.New("code is required")
	}
	sid, err := s.sessionMgr.ExchangeSessionCode(ctx, req.GetCode())
	if err != nil {
		return nil, xcodes.ErrSessionInvalid.Wrap(err)
	}
	data, err := s.sessionMgr.Get(ctx, sid)
	if err != nil {
		return nil, err
	}
	return &pb.ExchangeSessionCodeResponse{
		SessionId: sid,
		UserId:    data.UserID,
	}, nil
}

// ListSessions returns all active sessions for the current user.
//
// Uses GetMulti (single MGet round trip) instead of one Get per session. Side
// effect change: this list view no longer refreshes session TTLs — only
// validate-on-use paths (Get / GetSession / RefreshSession) do. See
// Manager.GetMulti for the rationale.
func (s *Service) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	userID := req.GetUserId()

	sessionIDs, err := s.sessionMgr.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(sessionIDs) == 0 {
		return &pb.ListSessionsResponse{}, nil
	}

	sessions, err := s.sessionMgr.GetMulti(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}

	pbSessions := make([]*pb.Session, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		data, ok := sessions[sid]
		if !ok {
			continue // expired between ListByUserID and GetMulti
		}
		pbSess := &pb.Session{
			Id:        sid,
			Ip:        data.LoginIP,
			Os:        data.OS,
			Browser:   data.Browser,
			CreatedAt: timestamppb.New(data.LoginAt),
		}
		pbSessions = append(pbSessions, pbSess)
	}

	return &pb.ListSessionsResponse{Sessions: pbSessions}, nil
}

// RevokeSession revokes a specific session.
func (s *Service) RevokeSession(ctx context.Context, req *pb.RevokeSessionRequest) (*emptypb.Empty, error) {
	// Caller is responsible for authorization; session_id uniquely identifies the session.
	// Pure read — we're about to revoke, so sliding the TTL window would be wasted work.
	data, err := s.sessionMgr.Get(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := s.revokeSession(ctx, req.GetSessionId(), data.UserID); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// RevokeAllSessions revokes all sessions for the current user.
func (s *Service) RevokeAllSessions(ctx context.Context, req *pb.RevokeAllSessionsRequest) (*emptypb.Empty, error) {
	userID := req.GetUserId()
	if err := s.revokeAllSessions(ctx, userID); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// --- internal helpers ---

// revokeSession revokes a session in both DB and Redis.
//
// Atomicity contract: Redis does not participate in the PostgreSQL
// transaction — once Redis writes, it cannot be rolled back. The two writes
// therefore follow "all-or-nothing" semantics inside the closure: if Redis
// fails, the DB tx rolls back and the session remains fully active; if the
// commit later fails after Redis succeeded, the session is over-revoked
// (rejected by Redis lookups even though DB still shows active), which is
// safe but loses audit accuracy. See README "一致性模型" for the broader
// discussion of cross-resource consistency.
func (s *Service) revokeSession(ctx context.Context, sessionID string, userID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.RevokeSession(ctx, tx, sessionID); err != nil {
			return err
		}
		return s.sessionMgr.Revoke(ctx, sessionID, userID)
	})
}

// revokeAllSessions revokes all sessions for a user in both DB and Redis.
// See revokeSession for the atomicity contract.
func (s *Service) revokeAllSessions(ctx context.Context, userID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := dal.RevokeAllUserSessions(ctx, tx, userID); err != nil {
			return err
		}
		return s.sessionMgr.RevokeAll(ctx, userID)
	})
}

// RevokeAllByUserID is the non-RPC entry point used by other subpackages (e.g.
// user.Service.DisableUser) to immediately invalidate every active session
// for a user without going through the RevokeAllSessions proto RPC.
func (s *Service) RevokeAllByUserID(ctx context.Context, userID int64) error {
	return s.revokeAllSessions(ctx, userID)
}

// --- duplicated helpers (cross-file deps, copied from package service) ---
// These will be reconciled when the original package service files are deleted in Task 2.7.
