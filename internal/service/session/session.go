package session

import (
	"context"
	"strconv"
	"strings"
	"time"

	pb "github.com/servekit/api/gen/go/user/v1"
	"github.com/servekit/user-service/internal/store/dal"
	"github.com/servekit/user-service/pkg/clientinfo"
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
		LoginTarget: data.LoginTarget,
		Device:      data.Device,
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
// validate-on-use paths (Get / GetSession) do. See
// Manager.GetMulti for the rationale.
//
// DeviceType derives from the stored OS/UserAgent (old sessions with no
// capture stay UNSPECIFIED). LastActiveAt derives from the per-user ZSET
// expiry score (score - TTL = last validate-on-use), clamped to at least
// LoginAt so freshly created sessions read sanely.
//
// Merged view, cursor-paged over history: the FIRST call (empty cursor)
// returns ACTIVE rows from Redis ahead of the first history page; subsequent
// calls return history only, strictly below the cursor (created_at desc).
// Historical last-active is unknowable (the column is gone) and stays unset;
// revoked_at distinguishes explicit logout from lapsed/evicted.
const defaultHistoryPageSize = 20

func (s *Service) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	userID := req.GetUserId()
	pageSize := req.GetPageSize()
	if pageSize <= 0 {
		pageSize = defaultHistoryPageSize
	}
	var beforeCreated time.Time
	if c := req.GetCursor(); c != "" {
		nano, err := strconv.ParseInt(c, 10, 64)
		if err != nil {
			return nil, xcodes.ErrBadRequest.Wrapf(err, "invalid cursor: %s", c)
		}
		beforeCreated = time.Unix(0, nano)
	}
	firstPage := beforeCreated.IsZero()
	statusFilter := req.GetStatus()

	sessionIDs, expiryScores, err := s.sessionMgr.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(sessionIDs))
	for _, sid := range sessionIDs {
		live[sid] = struct{}{}
	}

	pbSessions := make([]*pb.Session, 0, len(sessionIDs)+int(pageSize))
	emitLive := firstPage && statusFilter != pb.SessionStatus_SESSION_STATUS_REVOKED &&
		statusFilter != pb.SessionStatus_SESSION_STATUS_EXPIRED
	if emitLive && len(sessionIDs) > 0 {
		sessions, err := s.sessionMgr.GetMulti(ctx, sessionIDs)
		if err != nil {
			return nil, err
		}

		ttlSecs := int64(s.sessionMgr.TTL() / time.Second)
		for _, sid := range sessionIDs {
			data, ok := sessions[sid]
			if !ok {
				continue // expired between ListByUserID and GetMulti
			}
			lastActive := data.LoginAt
			if score, ok := expiryScores[sid]; ok {
				if t := time.Unix(int64(score)-ttlSecs, 0); t.After(lastActive) {
					lastActive = t
				}
			}
			pbSessions = append(pbSessions, &pb.Session{
				Id:           sid,
				Ip:           data.LoginIP,
				DeviceType:   deviceTypeFor(data),
				Os:           data.OS,
				Browser:      data.Browser,
				CreatedAt:    timestamppb.New(data.LoginAt),
				LastActiveAt: timestamppb.New(lastActive),
				Status:       pb.SessionStatus_SESSION_STATUS_ACTIVE,
				LoginMethod:  data.LoginMethod,
				LoginTarget:  data.LoginTarget,
				Device:       data.Device,
			})
		}
	}

	// History from the PG tombstones, newest first, skipping rows still live.
	// Fetch one extra row to detect whether another page exists. The ACTIVE
	// filter wants live rows only — skip the tombstone query entirely.
	if statusFilter == pb.SessionStatus_SESSION_STATUS_ACTIVE {
		return &pb.ListSessionsResponse{Sessions: pbSessions}, nil
	}
	var revokedOnly *bool
	switch statusFilter {
	case pb.SessionStatus_SESSION_STATUS_REVOKED:
		trueVal := true
		revokedOnly = &trueVal
	case pb.SessionStatus_SESSION_STATUS_EXPIRED:
		falseVal := false
		revokedOnly = &falseVal
	}
	fetch := int(pageSize) + len(live) + 1
	rows, err := dal.ListSessionsByUserID(ctx, s.db, userID, fetch, beforeCreated, revokedOnly)
	if err != nil {
		return nil, err
	}
	history := rows[:0:min(len(rows), fetch)]
	for _, r := range rows {
		if _, ok := live[r.ID]; ok {
			continue
		}
		history = append(history, r)
	}
	var nextCursor string
	if len(history) > int(pageSize) {
		history = history[:pageSize]
		nextCursor = strconv.FormatInt(history[len(history)-1].CreatedAt.UnixNano(), 10)
	}
	for _, r := range history {
		status := pb.SessionStatus_SESSION_STATUS_EXPIRED
		if r.RevokedAt != nil {
			status = pb.SessionStatus_SESSION_STATUS_REVOKED
		}
		pbSess := &pb.Session{
			Id:          r.ID,
			Ip:          r.IP,
			DeviceType:  pb.DeviceType(r.DeviceType),
			Os:          r.OS,
			Browser:     r.Browser,
			CreatedAt:   timestamppb.New(r.CreatedAt),
			Status:      status,
			LoginMethod: r.LoginMethod,
			LoginTarget: r.LoginTarget,
			Device:      r.Device,
		}
		// A revoked session's last lifecycle event IS the logout — surface
		// revoked_at as its final activity time. Lapsed/evicted rows have no
		// knowable moment and stay unset.
		if r.RevokedAt != nil {
			pbSess.LastActiveAt = timestamppb.New(*r.RevokedAt)
		}
		pbSessions = append(pbSessions, pbSess)
	}

	return &pb.ListSessionsResponse{Sessions: pbSessions, NextCursor: nextCursor}, nil
}

// deviceTypeFor classifies a stored session for the list view. Read-side
// semantics differ from login-time capture (common.LoginDeviceType): a
// session with NO captured environment is a legacy row from before client
// capture existed — UNSPECIFIED ("unknown") — not an API client, which only
// login-time capture can conclude.
func deviceTypeFor(data *Data) pb.DeviceType {
	switch {
	case strings.Contains(data.OS, "iOS"):
		return pb.DeviceType_DEVICE_TYPE_IOS
	case strings.Contains(data.OS, "Android"):
		return pb.DeviceType_DEVICE_TYPE_ANDROID
	case clientinfo.IsApiClient(data.UserAgent):
		return pb.DeviceType_DEVICE_TYPE_API
	case data.UserAgent != "":
		return pb.DeviceType_DEVICE_TYPE_WEB
	default:
		return pb.DeviceType_DEVICE_TYPE_UNSPECIFIED
	}
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
