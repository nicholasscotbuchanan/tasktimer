"""Login, self-registration, and key issuance.

There is one flow and it does all three. A user who has never been seen before
consents to Atlassian, and the account is created from the identity that comes
back — no admin provisions anything, no one is invited, nobody pastes a token
into a settings field. "Can you consent to our app against our Jira site" IS the
membership test, and it is one the server can actually verify.

The desktop client half is RFC 8252 (OAuth for native apps): a loopback redirect
plus PKCE. The client opens a listener on 127.0.0.1, sends its challenge here,
and gets back a one-time code it trades for the real key over TLS. The key never
travels in a URL, so it never lands in browser history or a proxy log.
"""

from __future__ import annotations

import secrets
from datetime import datetime, timedelta, timezone
from urllib.parse import urlencode, urlparse

from fastapi import APIRouter, Depends, HTTPException, Query, status
from fastapi.responses import HTMLResponse, RedirectResponse
from sqlalchemy import select
from sqlalchemy.orm import Session

from ..config import Settings, get_settings
from ..db import get_session
from ..jira import oauth
from ..models import ApiKey, AuthCode, JiraToken, PendingAuth, User
from ..schemas import ExchangeRequest, ExchangeResponse, Me
from ..security import current_user, new_api_key, pkce_verify
from ..service import http_client, store_tokens

router = APIRouter(tags=["auth"])

# Long enough that a user can read a consent screen and think about it; short
# enough that an abandoned login does not leave a redeemable row lying around.
PENDING_TTL = timedelta(minutes=10)
# The client is already listening when the browser redirects. This only has to
# survive the hop from browser to loopback to exchange call.
CODE_TTL = timedelta(minutes=2)


def _require_loopback(redirect_uri: str) -> None:
    """Refuse to redirect anywhere but the machine the user is sitting at.

    Without this the endpoint is an open redirect wearing a Jira costume: hand it
    ?redirect_uri=https://evil.example and it will hand a valid, redeemable code
    to whoever asked. Loopback only — the client is a desktop app.
    """
    parsed = urlparse(redirect_uri)
    if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "::1", "localhost"):
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="redirect_uri must be a loopback address, e.g. http://127.0.0.1:53682/callback",
        )


@router.get(
    "/auth/login",
    summary="Begin the Atlassian consent flow",
    description="Open in a browser. Redirects to Atlassian; the account is created on the "
    "way back if this is the user's first time.",
    response_class=RedirectResponse,
    status_code=status.HTTP_307_TEMPORARY_REDIRECT,
)
def login(
    redirect_uri: str = Query(description="The desktop client's loopback callback."),
    code_challenge: str = Query(description="PKCE S256 challenge (base64url, unpadded)."),
    state: str = Query(default="", description="Opaque value echoed back to the client."),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> RedirectResponse:
    settings.require_oauth()
    _require_loopback(redirect_uri)

    our_state = secrets.token_urlsafe(32)
    session.add(
        PendingAuth(
            state=our_state,
            code_challenge=code_challenge,
            client_redirect_uri=redirect_uri,
            client_state=state,
        )
    )

    return RedirectResponse(
        oauth.authorize_url(settings.atlassian_client_id, settings.redirect_uri, our_state),
        status_code=status.HTTP_307_TEMPORARY_REDIRECT,
    )


@router.get(
    "/auth/callback",
    summary="Atlassian redirects here after consent",
    description="Not called by the client. Exchanges the Atlassian code, creates the account "
    "if it is new, and bounces a one-time code back to the client's loopback listener.",
    include_in_schema=False,
    response_class=HTMLResponse,
)
async def callback(
    code: str = Query(default=""),
    state: str = Query(default=""),
    error: str = Query(default=""),
    error_description: str = Query(default=""),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
):
    if error:
        return _page("Jira connection cancelled", error_description or error, ok=False)

    pending = session.get(PendingAuth, state)
    if pending is None:
        # Either a replay, or a login left open past its TTL and finished later.
        return _page(
            "That login has expired",
            "Close this tab and press 'Connect to Jira' in Task Timer again.",
            ok=False,
        )
    session.delete(pending)

    if _expired(pending.created_at, PENDING_TTL):
        return _page(
            "That login has expired",
            "Close this tab and press 'Connect to Jira' in Task Timer again.",
            ok=False,
        )

    try:
        tokens = await oauth.exchange_code(
            http_client(),
            client_id=settings.atlassian_client_id,
            client_secret=settings.atlassian_client_secret,
            code=code,
            redirect_uri=settings.redirect_uri,
        )
        who = await oauth.identity(http_client(), tokens.access_token)
        site = await oauth.first_jira_site(http_client(), tokens.access_token)
    except oauth.JiraAuthError as exc:
        return _page("Could not connect to Jira", str(exc), ok=False)

    if not _domain_allowed(who.email, settings):
        return _page(
            "This account cannot register",
            f"{who.email or 'That account'} is not in an allowed domain for this server.",
            ok=False,
        )

    user = _upsert_user(session, who, tokens, site, settings)

    auth_code = secrets.token_urlsafe(32)
    session.add(
        AuthCode(
            code=auth_code,
            user_id=user.id,
            code_challenge=pending.code_challenge,
            expires_at=datetime.now(timezone.utc) + CODE_TTL,
        )
    )
    session.flush()

    query = urlencode({"code": auth_code, "state": pending.client_state})
    sep = "&" if "?" in pending.client_redirect_uri else "?"
    return RedirectResponse(
        f"{pending.client_redirect_uri}{sep}{query}",
        status_code=status.HTTP_303_SEE_OTHER,
    )


def _upsert_user(
    session: Session,
    who: oauth.AtlassianIdentity,
    tokens: oauth.TokenSet,
    site: oauth.JiraSite,
    settings: Settings,
) -> User:
    """Find the user, or register them. First consent creates the account."""
    user = session.scalar(select(User).where(User.atlassian_account_id == who.account_id))
    if user is None:
        user = User(
            atlassian_account_id=who.account_id,
            email=who.email,
            display_name=who.display_name,
        )
        session.add(user)
        session.flush()
    else:
        # People change their name and their email; the account id never changes.
        user.email = who.email or user.email
        user.display_name = who.display_name or user.display_name

    token = user.jira or JiraToken(user_id=user.id)
    token.cloud_id = site.cloud_id
    token.site_url = site.url
    store_tokens(session, token, tokens, settings)
    session.flush()
    return user


def _domain_allowed(email: str, settings: Settings) -> bool:
    if not settings.allowed_email_domains:
        return True
    domain = email.rsplit("@", 1)[-1].lower() if "@" in email else ""
    return domain in settings.allowed_email_domains


@router.post(
    "/api/v1/auth/exchange",
    tags=["auth"],
    summary="Trade the one-time code for an API key",
    response_model=ExchangeResponse,
)
def exchange(
    body: ExchangeRequest,
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> ExchangeResponse:
    record = session.get(AuthCode, body.code)
    if record is None:
        raise HTTPException(status_code=400, detail="That code is not valid.")

    # One shot, valid or not. Deleting before the checks below means a wrong
    # verifier burns the code rather than letting it be brute-forced.
    session.delete(record)

    if _expired_at(record.expires_at):
        raise HTTPException(status_code=400, detail="That code has expired. Connect again.")

    if not pkce_verify(body.code_verifier, record.code_challenge):
        raise HTTPException(status_code=400, detail="The PKCE verifier does not match.")

    user = session.get(User, record.user_id)
    if user is None:
        raise HTTPException(status_code=400, detail="That code is not valid.")

    raw, key_hash, prefix = new_api_key()
    session.add(ApiKey(user_id=user.id, key_hash=key_hash, prefix=prefix, label="desktop"))
    session.flush()

    return ExchangeResponse(api_key=raw, user=_me(user))


@router.get("/api/v1/me", tags=["auth"], summary="Who am I, and is Jira connected?", response_model=Me)
def me(user: User = Depends(current_user)) -> Me:
    return _me(user)


def _me(user: User) -> Me:
    return Me(
        email=user.email,
        display_name=user.display_name,
        jira_connected=user.jira is not None,
        jira_site_url=user.jira.site_url if user.jira else "",
    )


# ---------------------------------------------------------------------------


def _expired(created_at: datetime | None, ttl: timedelta) -> bool:
    if created_at is None:
        return False
    if created_at.tzinfo is None:
        created_at = created_at.replace(tzinfo=timezone.utc)
    return datetime.now(timezone.utc) - created_at > ttl


def _expired_at(when: datetime) -> bool:
    if when.tzinfo is None:
        when = when.replace(tzinfo=timezone.utc)
    return datetime.now(timezone.utc) > when


def _page(title: str, message: str, *, ok: bool) -> HTMLResponse:
    """The only HTML this server serves: what the user sees after consenting.

    They are looking at a browser tab that the desktop app opened. Ending the
    flow on a raw JSON body, or on nothing at all, is how a user is left unsure
    whether it worked.
    """
    colour = "#1f883d" if ok else "#cf222e"
    return HTMLResponse(
        f"""<!doctype html>
<meta charset="utf-8">
<title>Task Timer</title>
<style>
  body {{ font: 16px/1.5 system-ui, sans-serif; margin: 0; display: grid;
         place-items: center; min-height: 100vh; background: #f6f8fa; color: #1f2328; }}
  .card {{ background: #fff; padding: 2.5rem 3rem; border-radius: 12px; max-width: 30rem;
           box-shadow: 0 1px 3px rgba(0,0,0,.12); text-align: center; }}
  h1 {{ margin: 0 0 .5rem; font-size: 1.25rem; color: {colour}; }}
  p {{ margin: 0; color: #656d76; }}
  @media (prefers-color-scheme: dark) {{
    body {{ background: #0d1117; color: #e6edf3; }}
    .card {{ background: #161b22; box-shadow: none; }}
    p {{ color: #8b949e; }}
  }}
</style>
<div class="card"><h1>{title}</h1><p>{message}</p></div>""",
        status_code=200 if ok else 400,
    )
