-- Account kinds and OIDC admin sign-on (design doc §7.5).
-- kind 'oidc' users have password_hash '!' (never verifies), no tokens and no Basic auth.
ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'local' CHECK (kind IN ('local','oidc'));
ALTER TABLE users ADD COLUMN oidc_issuer TEXT;
ALTER TABLE users ADD COLUMN oidc_subject TEXT;
ALTER TABLE users ADD COLUMN email TEXT;
CREATE UNIQUE INDEX users_oidc ON users(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;
ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'personal' CHECK (kind IN ('personal','minted'));
