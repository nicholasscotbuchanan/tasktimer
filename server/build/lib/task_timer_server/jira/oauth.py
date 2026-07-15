"""Atlassian OAuth 2.0 (3LO).

Three things about Atlassian's implementation drive the shape of this module:

  - The refresh token ROTATES. Every refresh returns a new one and kills the old
    one immediately. Persisting the new value is not an optimisation; skipping it
    locks the user out for good.
  - `offline_access` is what makes a refresh token appear at all. Without that
    scope the grant dies in an hour and the user is asked to consent again.
  - The Jira REST API is NOT at the site's own hostname under 3LO. It is at
    api.atlassian.com/ex/jira/<cloud_id>, and the cloud_id has to be discovered
    from /oauth/token/accessible-resources after the exchange.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode

import httpx

AUTHORIZE_URL = "https://auth.atlassian.com/authorize"
TOKEN_URL = "https://auth.atlassian.com/oauth/token"
RESOURCES_URL = "https://api.atlassian.com/oauth/token/accessible-resources"
ME_URL = "https://api.atlassian.com/me"

# read/write:jira-work covers issue search, work logs and transitions.
# offline_access is what buys us a refresh token.
SCOPES = ("read:me", "read:jira-work", "write:jira-work", "offline_access")

# Refresh a little early. An access token that expires between our check and
# Jira's receipt of the request is a 401 the user never needed to see.
REFRESH_SKEW = timedelta(seconds=90)

TIMEOUT = httpx.Timeout(30.0)


@dataclass(slots=True)
class TokenSet:
    access_token: str
    refresh_token: str
    expires_at: datetime


@dataclass(slots=True)
class AtlassianIdentity:
    account_id: str
    email: str
    display_name: str


@dataclass(slots=True)
class JiraSite:
    cloud_id: str
    url: str


def authorize_url(client_id: str, redirect_uri: str, state: str) -> str:
    """The URL the user's browser is sent to in order to consent."""
    query = urlencode(
        {
            "audience": "api.atlassian.com",
            "client_id": client_id,
            "scope": " ".join(SCOPES),
            "redirect_uri": redirect_uri,
            "state": state,
            "response_type": "code",
            # Atlassian only issues a refresh token when it is asked to consent
            # afresh; without this an already-consented user silently comes back
            # with no refresh token and the grant expires in an hour.
            "prompt": "consent",
        }
    )
    return f"{AUTHORIZE_URL}?{query}"


def _token_set(payload: dict, fallback_refresh: str = "") -> TokenSet:
    expires_in = int(payload.get("expires_in", 3600))
    return TokenSet(
        access_token=payload["access_token"],
        # Belt and braces: the spec permits omitting an unchanged refresh token,
        # and dropping it on the floor here would be indistinguishable from a
        # revoked grant the next time we tried to refresh.
        refresh_token=payload.get("refresh_token") or fallback_refresh,
        expires_at=datetime.now(timezone.utc) + timedelta(seconds=expires_in),
    )


async def exchange_code(
    client: httpx.AsyncClient,
    *,
    client_id: str,
    client_secret: str,
    code: str,
    redirect_uri: str,
) -> TokenSet:
    resp = await client.post(
        TOKEN_URL,
        json={
            "grant_type": "authorization_code",
            "client_id": client_id,
            "client_secret": client_secret,
            "code": code,
            "redirect_uri": redirect_uri,
        },
        timeout=TIMEOUT,
    )
    _raise_for_oauth(resp, "exchanging the authorization code")
    return _token_set(resp.json())


async def refresh(
    client: httpx.AsyncClient,
    *,
    client_id: str,
    client_secret: str,
    refresh_token: str,
) -> TokenSet:
    resp = await client.post(
        TOKEN_URL,
        json={
            "grant_type": "refresh_token",
            "client_id": client_id,
            "client_secret": client_secret,
            "refresh_token": refresh_token,
        },
        timeout=TIMEOUT,
    )
    _raise_for_oauth(resp, "refreshing the access token")
    return _token_set(resp.json(), fallback_refresh=refresh_token)


async def identity(client: httpx.AsyncClient, access_token: str) -> AtlassianIdentity:
    resp = await client.get(
        ME_URL,
        headers={"Authorization": f"Bearer {access_token}", "Accept": "application/json"},
        timeout=TIMEOUT,
    )
    _raise_for_oauth(resp, "reading the Atlassian profile")
    body = resp.json()
    return AtlassianIdentity(
        account_id=body["account_id"],
        email=body.get("email", ""),
        display_name=body.get("name", ""),
    )


async def first_jira_site(client: httpx.AsyncClient, access_token: str) -> JiraSite:
    """Resolve the cloud id every subsequent Jira call is addressed to.

    A user may have access to several Atlassian sites. We take the first, which
    is right for the single-tenant deployment this server is built for; a
    multi-site rollout would let the user pick, and that choice would live on the
    JiraToken row beside the cloud id it already stores.
    """
    resp = await client.get(
        RESOURCES_URL,
        headers={"Authorization": f"Bearer {access_token}", "Accept": "application/json"},
        timeout=TIMEOUT,
    )
    _raise_for_oauth(resp, "listing accessible Atlassian sites")

    sites = resp.json()
    if not sites:
        raise JiraAuthError(
            "That Atlassian account has no Jira site available to this app. Ask an "
            "administrator to grant it access, then connect again."
        )
    return JiraSite(cloud_id=sites[0]["id"], url=sites[0].get("url", ""))


class JiraAuthError(RuntimeError):
    """The OAuth grant is unusable and the user has to reconnect."""


def _raise_for_oauth(resp: httpx.Response, what: str) -> None:
    if resp.is_success:
        return
    # Atlassian's OAuth errors are small and specific ("invalid_grant"), and they
    # are the whole diagnosis. Passing them through beats "unexpected status 400".
    detail = ""
    try:
        body = resp.json()
        detail = body.get("error_description") or body.get("error") or ""
    except ValueError:
        detail = resp.text[:200]
    raise JiraAuthError(f"{what} failed: Atlassian returned {resp.status_code}: {detail}".strip())
