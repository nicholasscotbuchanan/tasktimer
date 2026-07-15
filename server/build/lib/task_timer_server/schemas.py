"""Request and response models.

These are the API's documentation. Every description and example here surfaces
in the OpenAPI schema at /docs, which is the whole reason the backend is FastAPI
rather than something that needs a hand-written spec kept in step by discipline.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field


class LoginStart(BaseModel):
    """Where to send the user's browser to connect (or create) their account."""

    authorize_url: str = Field(
        description="Open this in a browser. It leads to Atlassian's consent screen.",
        examples=["https://auth.atlassian.com/authorize?..."],
    )


class ExchangeRequest(BaseModel):
    """Trade the one-time code from the loopback redirect for a lasting API key."""

    code: str = Field(description="The `code` query parameter delivered to the loopback redirect.")
    code_verifier: str = Field(
        description="The PKCE verifier whose challenge was sent to /auth/login.",
        min_length=43,
        max_length=128,
    )


class ExchangeResponse(BaseModel):
    api_key: str = Field(
        description="Send as `Authorization: Bearer <api_key>`. Shown exactly once — "
        "the server keeps only a hash and cannot show it again.",
        examples=["tt_x7Kd9..."],
    )
    user: "Me"


class Me(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    email: str
    display_name: str
    jira_connected: bool = Field(
        description="False once the Atlassian grant has been revoked or has expired; "
        "the client should prompt to reconnect."
    )
    jira_site_url: str = ""


class Task(BaseModel):
    """A Jira issue, as the desktop client's task list wants it."""

    key: str = Field(examples=["ENG-412"])
    title: str = Field(examples=["Timer drifts when the laptop sleeps"])
    url: str = Field(examples=["https://acme.atlassian.net/browse/ENG-412"])
    status: str = Field(examples=["In Progress"])
    assigned_by: str = Field(default="", examples=["Dana Rivera"])
    done: bool = Field(
        description="From Jira's statusCategory, not the status name — status names "
        "differ per project and are not comparable across boards."
    )
    updated_at: datetime | None = None


class TaskList(BaseModel):
    tasks: list[Task]


class WorklogRequest(BaseModel):
    """A completed local timer session, on its way to Jira."""

    issue_key: str = Field(examples=["ENG-412"])
    started: datetime = Field(
        description="When the session began. Send it with its timezone offset; a naive "
        "timestamp is read as the server's local time, which is rarely what you meant."
    )
    duration_seconds: int = Field(
        gt=0,
        description="Jira will not accept a work log under 60 seconds. Shorter sessions are "
        "rounded UP to 60 rather than dropped — a session the user deliberately timed "
        "should still appear on the issue.",
        examples=[1500],
    )
    comment: str = Field(default="", examples=["Traced the drift to the monotonic clock."])
    idempotency_key: str = Field(
        description="A stable id the client generates per session and REUSES on every retry. "
        "Pushing the same key twice returns the first push's work-log id instead of "
        "logging the session to Jira a second time.",
        min_length=8,
        max_length=128,
        examples=["7f3c1e0a-2b44-4d9e-9f16-0a1b2c3d4e5f"],
    )


class WorklogResponse(BaseModel):
    jira_worklog_id: str = Field(description="Jira's id for the work log.")
    issue_key: str
    duplicate: bool = Field(
        description="True when this idempotency key had already been pushed, and nothing "
        "was written to Jira on this request."
    )


class CompleteResponse(BaseModel):
    issue_key: str
    transitioned: bool


class Problem(BaseModel):
    """The error body every 4xx and 5xx carries."""

    detail: str
