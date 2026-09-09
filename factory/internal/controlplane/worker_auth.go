package controlplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const WorkerEnrollmentLifetime = 10 * time.Minute

func randomWorkerSecret(prefix string) (string, error) {
	body := make([]byte, 32)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(body), nil
}

func (s *Store) CreateWorkerEnrollment(ctx context.Context, workerID string) (protocol.WorkerEnrollment, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 {
		return protocol.WorkerEnrollment{}, invalid("invalid_worker_id", "worker_id is required and must be at most 200 bytes")
	}
	var synthetic int
	if err := s.db.QueryRowContext(ctx, `SELECT synthetic FROM workers WHERE id = ?`, workerID).Scan(&synthetic); err == nil && synthetic != 0 {
		return protocol.WorkerEnrollment{}, conflict("synthetic_worker_isolated", "synthetic cloud Workers cannot enroll")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkerEnrollment{}, unavailable(err)
	}
	token, err := randomWorkerSecret("factory_enroll_")
	if err != nil {
		return protocol.WorkerEnrollment{}, unavailable(err)
	}
	id, err := newID()
	if err != nil {
		return protocol.WorkerEnrollment{}, unavailable(err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(WorkerEnrollmentLifetime)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO worker_enrollments(id, worker_id, token_digest, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, workerID, digestToken(token), expiresAt.UnixMilli(), now.UnixMilli()); err != nil {
		return protocol.WorkerEnrollment{}, unavailable(err)
	}
	return protocol.WorkerEnrollment{WorkerID: workerID, EnrollmentToken: token, ExpiresAt: expiresAt}, nil
}

func (s *Store) ExchangeWorkerEnrollment(
	ctx context.Context,
	workerID, enrollmentToken, credential string,
) (protocol.WorkerCredential, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 || len(enrollmentToken) < 32 || len(enrollmentToken) > 1024 ||
		len(credential) < 32 || len(credential) > 1024 || credential != strings.TrimSpace(credential) ||
		(!strings.HasPrefix(credential, "factory_worker_") &&
			!strings.HasPrefix(credential, "factory_runner_")) {
		return protocol.WorkerCredential{}, unauthorizedWorker()
	}
	now := s.now().UTC().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	defer tx.Rollback()
	var synthetic int
	if err := tx.QueryRowContext(ctx, `SELECT synthetic FROM workers WHERE id = ?`, workerID).Scan(&synthetic); err == nil && synthetic != 0 {
		return protocol.WorkerCredential{}, unauthorizedWorker()
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	var enrollmentID string
	var usedAt sql.NullInt64
	var expiresAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, used_at, expires_at FROM worker_enrollments
		WHERE worker_id = ? AND token_digest = ?
	`, workerID, digestToken(enrollmentToken)).Scan(&enrollmentID, &usedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkerCredential{}, unauthorizedWorker()
	}
	if err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	if usedAt.Valid {
		var storedDigest []byte
		err = tx.QueryRowContext(ctx, `
			SELECT token_digest FROM remote_worker_credentials WHERE worker_id = ?
		`, workerID).Scan(&storedDigest)
		if err != nil || !equalDigest(storedDigest, digestToken(credential)) {
			return protocol.WorkerCredential{}, unauthorizedWorker()
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkerCredential{}, unavailable(err)
		}
		return protocol.WorkerCredential{Credential: credential}, nil
	}
	if !strings.HasPrefix(credential, "factory_worker_") {
		return protocol.WorkerCredential{}, invalid(
			"worker_credential_regeneration_required",
			"the pending Worker credential must be regenerated before this enrollment can be exchanged",
		)
	}
	if expiresAt < now {
		return protocol.WorkerCredential{}, unauthorizedWorker()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE worker_enrollments SET used_at = ?
		WHERE id = ? AND used_at IS NULL AND expires_at >= ?
	`, now, enrollmentID, now)
	if err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	} else if count != 1 {
		return protocol.WorkerCredential{}, unauthorizedWorker()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO remote_worker_credentials(worker_id, token_digest, created_at, last_used_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(worker_id) DO UPDATE SET
			token_digest = excluded.token_digest,
			created_at = excluded.created_at,
			last_used_at = excluded.last_used_at
	`, workerID, digestToken(credential), now, now); err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkerCredential{}, unavailable(err)
	}
	return protocol.WorkerCredential{Credential: credential}, nil
}

func (s *Store) AuthenticateWorkerCredential(ctx context.Context, credential string) (string, error) {
	if len(credential) < 32 || len(credential) > 1024 {
		return "", unauthorizedWorker()
	}
	var workerID string
	err := s.db.QueryRowContext(ctx, `
		SELECT credential.worker_id
		FROM remote_worker_credentials credential
		WHERE credential.token_digest = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM workers worker
		      WHERE worker.id = credential.worker_id AND worker.synthetic = 1
		  )
	`, digestToken(credential)).Scan(&workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", unauthorizedWorker()
	}
	if err != nil {
		return "", unavailable(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE remote_worker_credentials SET last_used_at = ? WHERE worker_id = ?
	`, s.now().UTC().UnixMilli(), workerID); err != nil {
		return "", unavailable(err)
	}
	return workerID, nil
}

func unauthorizedWorker() error {
	return &ServiceError{Code: "worker_unauthorized", Message: "valid Worker credentials are required", Status: 401}
}
