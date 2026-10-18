-- Sessions now slide an idle expiry forward with use, bounded by a fixed
-- maximum age from login. Tokens are stored as SHA-256 digests from this
-- version on, so rows written by earlier versions can no longer be presented;
-- their zero maximum also marks them expired, and login prunes them.
ALTER TABLE sessions ADD COLUMN max_expires_at_unix INTEGER NOT NULL DEFAULT 0;
