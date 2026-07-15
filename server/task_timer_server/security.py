"""Keys, hashing, encryption at rest, and the bearer-auth dependency."""

from __future__ import annotations

import base64
import hashlib
import hmac
import secrets
from datetime import datetime, timezone

from cryptography.fernet import Fernet, InvalidToken
from fastapi import Depends, HTTPException, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from sqlalchemy import select
from sqlalchemy.orm import Session

from .config import Settings, get_settings
from .db import get_session
from .models import ApiKey, User

# A visible prefix makes a leaked key greppable in logs and recognisable to
# secret scanners, which is the difference between a key that gets revoked and
# one that sits in a paste bin for a year.
KEY_PREFIX = "tt_"
KEY_BYTES = 32

bearer_scheme = HTTPBearer(auto_error=False, description="The key issued by /api/v1/auth/exchange.")


# ---------------------------------------------------------------------------
# API keys
# ---------------------------------------------------------------------------


def new_api_key() -> tuple[str, str, str]:
    """Return (plaintext, sha256 hash, public prefix). The plaintext is shown once."""
    raw = KEY_PREFIX + secrets.token_urlsafe(KEY_BYTES)
    return raw, hash_api_key(raw), raw[: len(KEY_PREFIX) + 6]


def hash_api_key(raw: str) -> str:
    # A plain SHA-256, not a password KDF, and deliberately so: this is a 256-bit
    # random value, not a human-chosen password. There is no dictionary to attack
    # and nothing for bcrypt's work factor to buy — it would only make every
    # authenticated request slower.
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Encryption at rest
# ---------------------------------------------------------------------------


def _fernet(settings: Settings) -> Fernet:
    key = settings.token_encryption_key.strip()
    if not key:
        raise RuntimeError("token_encryption_key is not set; refusing to store Jira tokens")
    try:
        return Fernet(key.encode("utf-8"))
    except (ValueError, TypeError) as exc:
        raise RuntimeError(
            "token_encryption_key is not a valid Fernet key. Generate one with:\n"
            "  python -c \"from cryptography.fernet import Fernet; "
            'print(Fernet.generate_key().decode())"'
        ) from exc


def encrypt(value: str, settings: Settings | None = None) -> str:
    return _fernet(settings or get_settings()).encrypt(value.encode("utf-8")).decode("ascii")


def decrypt(value: str, settings: Settings | None = None) -> str:
    try:
        return _fernet(settings or get_settings()).decrypt(value.encode("ascii")).decode("utf-8")
    except InvalidToken as exc:
        # Almost always means the encryption key was rotated or regenerated out
        # from under the database. Say so, rather than emitting a bare 500 that
        # sends someone hunting through Atlassian's logs for a fault that is ours.
        raise RuntimeError(
            "could not decrypt a stored Jira token: the token_encryption_key does not "
            "match the one the token was encrypted with. If the key was rotated, affected "
            "users must reconnect Jira."
        ) from exc


# ---------------------------------------------------------------------------
# PKCE (RFC 7636), on the client <-> backend leg
# ---------------------------------------------------------------------------


def pkce_challenge(verifier: str) -> str:
    digest = hashlib.sha256(verifier.encode("ascii")).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


def pkce_verify(verifier: str, challenge: str) -> bool:
    return hmac.compare_digest(pkce_challenge(verifier), challenge)


# ---------------------------------------------------------------------------
# The authenticated-user dependency
# ---------------------------------------------------------------------------


def current_user(
    credentials: HTTPAuthorizationCredentials | None = Depends(bearer_scheme),
    session: Session = Depends(get_session),
) -> User:
    if credentials is None or not credentials.credentials:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Missing bearer token. Run the desktop client's 'Connect to Jira' once.",
            headers={"WWW-Authenticate": "Bearer"},
        )

    key = session.scalar(
        select(ApiKey).where(ApiKey.key_hash == hash_api_key(credentials.credentials))
    )
    if key is None or key.revoked_at is not None:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="That API key is not valid.",
            headers={"WWW-Authenticate": "Bearer"},
        )

    key.last_used_at = datetime.now(timezone.utc)
    return key.user
