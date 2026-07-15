"""Persistence model.

Four things live here and nothing else does: who a user is, the Atlassian tokens
we hold on their behalf, the bearer keys their clients authenticate with, and a
record of which work logs we have already pushed.

That last table is the one that earns its keep. The desktop client is the system
of record for local timing and it retries; without a dedup key, a retry after a
response we never saw would log the same session to Jira twice, and Jira will
happily accept it.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import (
    DateTime,
    ForeignKey,
    Integer,
    String,
    Text,
    UniqueConstraint,
    func,
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, relationship


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Base(DeclarativeBase):
    pass


class User(Base):
    """A person, identified by their Atlassian account.

    There is no password and no local registration form. The row is created the
    first time someone completes the Atlassian consent flow — that consent *is*
    the registration.
    """

    __tablename__ = "users"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    atlassian_account_id: Mapped[str] = mapped_column(String(128), unique=True, index=True)
    email: Mapped[str] = mapped_column(String(320), index=True)
    display_name: Mapped[str] = mapped_column(String(256), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())

    jira: Mapped["JiraToken"] = relationship(
        back_populates="user", uselist=False, cascade="all, delete-orphan"
    )
    api_keys: Mapped[list["ApiKey"]] = relationship(
        back_populates="user", cascade="all, delete-orphan"
    )


class JiraToken(Base):
    """The Atlassian OAuth grant we hold for one user.

    Both tokens are stored encrypted (see security.encrypt/decrypt). Atlassian
    ROTATES the refresh token on every refresh — the old one dies the moment a
    new one is issued — so a failure to persist the new value locks the user out
    permanently. Every refresh writes back in the same transaction that uses it.
    """

    __tablename__ = "jira_tokens"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"), unique=True)

    access_token_enc: Mapped[str] = mapped_column(Text)
    refresh_token_enc: Mapped[str] = mapped_column(Text)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))

    # Discovered from /oauth/token/accessible-resources. Every Jira call goes to
    # api.atlassian.com/ex/jira/<cloud_id>, never to the site's own hostname.
    cloud_id: Mapped[str] = mapped_column(String(64))
    site_url: Mapped[str] = mapped_column(String(512), default="")

    user: Mapped[User] = relationship(back_populates="jira")


class ApiKey(Base):
    """A bearer key one desktop client authenticates with.

    Only the SHA-256 hash is kept. A leaked database therefore yields no working
    keys, and there is no way — including for us — to show a user their key
    again after it is issued.
    """

    __tablename__ = "api_keys"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"), index=True)

    key_hash: Mapped[str] = mapped_column(String(64), unique=True, index=True)
    # The public, non-secret leading chunk, so a user can tell two keys apart in
    # a list without either of them being usable from that list.
    prefix: Mapped[str] = mapped_column(String(16))
    label: Mapped[str] = mapped_column(String(128), default="")

    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    last_used_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)

    user: Mapped[User] = relationship(back_populates="api_keys")


class PendingAuth(Base):
    """One in-flight login, from /auth/login until /auth/callback.

    Carries the desktop client's PKCE challenge and its loopback redirect. Both
    are checked on the way back out: the challenge stops a local process that
    raced to the callback URL from redeeming a code it did not request, and the
    redirect is re-validated as loopback so a doctored ?redirect_uri cannot turn
    this endpoint into an open redirect.
    """

    __tablename__ = "pending_auth"

    state: Mapped[str] = mapped_column(String(64), primary_key=True)
    code_challenge: Mapped[str] = mapped_column(String(128))
    client_redirect_uri: Mapped[str] = mapped_column(String(512))
    client_state: Mapped[str] = mapped_column(String(128), default="")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())


class AuthCode(Base):
    """A one-time code handed to the desktop client over the loopback redirect.

    The bearer key is NOT put in that redirect URL. A URL lands in browser
    history, in any proxy's access log, and in the Referer of whatever the page
    loads next. The client trades this short-lived code for the key over TLS, in
    a request body, proving possession of the PKCE verifier as it does so.
    """

    __tablename__ = "auth_codes"

    code: Mapped[str] = mapped_column(String(64), primary_key=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"))
    code_challenge: Mapped[str] = mapped_column(String(128))
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class PushedWorklog(Base):
    """Proof that one client-side session already reached Jira.

    Keyed by an idempotency key the client generates and reuses across retries.
    A second push with the same key returns the first push's Jira work-log id
    instead of creating another one.
    """

    __tablename__ = "pushed_worklogs"
    __table_args__ = (UniqueConstraint("user_id", "idempotency_key"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"), index=True)

    idempotency_key: Mapped[str] = mapped_column(String(128), index=True)
    issue_key: Mapped[str] = mapped_column(String(64))
    jira_worklog_id: Mapped[str] = mapped_column(String(64))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
