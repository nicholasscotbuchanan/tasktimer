"""Binds a stored OAuth grant to a usable Jira client.

This is the only place that decides when to refresh, and it is the only place
that writes a rotated refresh token back. Atlassian kills the old refresh token
the instant it issues a new one, so a refresh whose result is not persisted
leaves the user permanently disconnected with no way to tell why.
"""

from __future__ import annotations

from datetime import datetime, timezone

import httpx
from fastapi import HTTPException, status
from sqlalchemy.orm import Session

from .config import Settings
from .jira import oauth
from .jira.client import JiraClient
from .models import JiraToken, User
from .security import decrypt, encrypt

# One connection pool for the whole process. Rebuilding a client (and its TLS
# handshakes) per request is a self-inflicted latency tax on every push.
_http: httpx.AsyncClient | None = None


def http_client() -> httpx.AsyncClient:
    global _http
    if _http is None:
        _http = httpx.AsyncClient(follow_redirects=False)
    return _http


async def close_http_client() -> None:
    global _http
    if _http is not None:
        await _http.aclose()
        _http = None


async def jira_for(session: Session, user: User, settings: Settings) -> JiraClient:
    """A Jira client authenticated as `user`, refreshing the grant if it is stale."""
    token: JiraToken | None = user.jira
    if token is None:
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT,
            detail="This account has not connected Jira yet. Run 'Connect to Jira' in the "
            "desktop client's Settings.",
        )

    expires_at = token.expires_at
    if expires_at.tzinfo is None:
        # SQLite hands back naive datetimes even for a timezone-aware column, and
        # comparing one of those to an aware `now` raises rather than misbehaving
        # quietly — which is the only reason this is worth a line of code.
        expires_at = expires_at.replace(tzinfo=timezone.utc)

    if expires_at - oauth.REFRESH_SKEW <= datetime.now(timezone.utc):
        await _refresh(session, token, settings)

    return JiraClient(
        http=http_client(),
        access_token=decrypt(token.access_token_enc, settings),
        cloud_id=token.cloud_id,
        site_url=token.site_url,
    )


async def _refresh(session: Session, token: JiraToken, settings: Settings) -> None:
    try:
        fresh = await oauth.refresh(
            http_client(),
            client_id=settings.atlassian_client_id,
            client_secret=settings.atlassian_client_secret,
            refresh_token=decrypt(token.refresh_token_enc, settings),
        )
    except oauth.JiraAuthError as exc:
        # invalid_grant here means the user revoked us, an admin removed their
        # access, or the grant simply aged out. None of those are retryable and
        # all of them are fixed the same way, so say so instead of surfacing an
        # Atlassian error code to someone who cannot act on it.
        raise HTTPException(
            status_code=status.HTTP_409_CONFLICT,
            detail="The Jira connection for this account is no longer valid. Run "
            "'Connect to Jira' in the desktop client's Settings to reconnect.",
        ) from exc

    store_tokens(session, token, fresh, settings)
    session.flush()


def store_tokens(
    session: Session, token: JiraToken, fresh: oauth.TokenSet, settings: Settings
) -> None:
    token.access_token_enc = encrypt(fresh.access_token, settings)
    token.refresh_token_enc = encrypt(fresh.refresh_token, settings)
    token.expires_at = fresh.expires_at
    session.add(token)
