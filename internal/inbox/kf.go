package inbox

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 游标与回调令牌分别更新，拉取过程中到达的新通知不会被旧请求覆盖。
type KFSyncState struct {
	Cursor, Token, NextAt, LastSuccess, Error string
	TokenAt                                   int64
	Generation, Attempts                      int
}

func (s *Store) EnsureKF(ctx context.Context, source string) error {
	_, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO kf_sync(source_id) VALUES(?)", source)
	return err
}

func (s *Store) KFNotify(ctx context.Context, source, token string, at int64) error {
	if err := s.EnsureKF(ctx, source); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE kf_sync SET token=?,token_at=?,generation=generation+1,next_at='' WHERE source_id=? AND token_at<=?`, s.seal([]byte(token)), at, source, at)
	return err
}

func (s *Store) KFState(ctx context.Context, source string) (KFSyncState, error) {
	var v KFSyncState
	var token []byte
	err := s.DB.QueryRowContext(ctx, `SELECT cursor,token,token_at,generation,next_at,last_success,error,attempts FROM kf_sync WHERE source_id=?`, source).Scan(&v.Cursor, &token, &v.TokenAt, &v.Generation, &v.NextAt, &v.LastSuccess, &v.Error, &v.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if len(token) > 0 {
		plain, e := s.unseal(token)
		if e != nil {
			return v, errors.New("customer-service notification token cannot be decrypted")
		}
		v.Token = string(plain)
	}
	return v, nil
}

func (s *Store) KFAdvance(ctx context.Context, source string, previous KFSyncState, cursor string, more bool) error {
	delay := 60 * time.Second
	if more {
		delay = 250 * time.Millisecond
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE kf_sync SET cursor=?,last_success=?,error='',attempts=0,next_at=CASE WHEN generation=? THEN ? ELSE next_at END WHERE source_id=? AND cursor=?`, cursor, now(), previous.Generation, time.Now().Add(delay).UTC().Format(timestampLayout), source, previous.Cursor)
	return err
}

func (s *Store) KFSyncFailure(ctx context.Context, source string, previous KFSyncState, failure string) error {
	delay := time.Duration(1<<min(previous.Attempts+1, 8)) * time.Second
	_, err := s.DB.ExecContext(ctx, `UPDATE kf_sync SET error=?,attempts=attempts+1,next_at=CASE WHEN generation=? THEN ? ELSE next_at END WHERE source_id=?`, failure, previous.Generation, time.Now().Add(delay).UTC().Format(timestampLayout), source)
	return err
}

func (s *Store) KFSendFailure(ctx context.Context, source, msgID, failure string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE replies SET status='failed',error=? WHERE id=(SELECT reply_id FROM kf_outbound WHERE source_id=? AND msgid=?)`, failure, source, msgID)
	return err
}
