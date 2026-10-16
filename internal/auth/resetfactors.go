package auth

import (
	"context"
	"database/sql"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
)

// ResetSecondFactors removes every TOTP factor, passkey and recovery code of a user, for a user
// who lost all of them (CLI "telesphorepo-server user reset-2fa"). The user's next sign-in, and every
// session the user still has, reaches only the setup routes until a new factor is added.
// It returns how many TOTP factors and passkeys it removed; ErrNotFound when there is no such
// user. It records user.update.
func (s *Service) ResetSecondFactors(ctx context.Context, username string) (totp, passkeys int64, err error) {
	u, err := s.GetUser(ctx, username)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		var err error
		if totp, err = q.DeleteAllUserTOTP(ctx, u.ID); err != nil {
			return err
		}
		if passkeys, err = q.DeleteAllUserPasskeys(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteUserRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.UserUpdate, Target: u.Username, Detail: map[string]any{"second_factors": "reset"}})
	})
	return totp, passkeys, err
}
