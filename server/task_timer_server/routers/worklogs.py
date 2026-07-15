"""Pushing a timed session to Jira.

The client is the system of record for local timing and it retries on its own
schedule — after a network drop, after a laptop lid closes mid-request, after a
restart. So this endpoint has to be safe to call twice with the same session,
because it WILL be. The client's idempotency key is what makes that safe: a
repeat returns the first push's work-log id and touches Jira not at all.

Jira itself offers no such guarantee. It will cheerfully accept the same work log
five times and put five entries on the issue, and there is no way to take them
back other than by hand.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from ..config import Settings, get_settings
from ..db import get_session
from ..jira import JiraError, WorkLog
from ..models import PushedWorklog, User
from ..schemas import WorklogRequest, WorklogResponse
from ..security import current_user
from ..service import jira_for
from .tasks import _upstream

router = APIRouter(prefix="/api/v1", tags=["worklogs"])


@router.post(
    "/worklogs",
    response_model=WorklogResponse,
    status_code=status.HTTP_201_CREATED,
    summary="Log a timed session against an issue",
    description="Idempotent on `idempotency_key`. Safe to retry: a repeat returns the "
    "original work-log id and writes nothing to Jira.",
)
async def push_worklog(
    body: WorklogRequest,
    user: User = Depends(current_user),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> WorklogResponse:
    seen = session.scalar(
        select(PushedWorklog).where(
            PushedWorklog.user_id == user.id,
            PushedWorklog.idempotency_key == body.idempotency_key,
        )
    )
    if seen is not None:
        return WorklogResponse(
            jira_worklog_id=seen.jira_worklog_id, issue_key=seen.issue_key, duplicate=True
        )

    client = await jira_for(session, user, settings)
    try:
        worklog_id = await client.add_worklog(
            WorkLog(
                issue_key=body.issue_key,
                started=body.started,
                duration_seconds=body.duration_seconds,
                comment=body.comment,
            )
        )
    except JiraError as exc:
        raise _upstream(exc) from exc

    session.add(
        PushedWorklog(
            user_id=user.id,
            idempotency_key=body.idempotency_key,
            issue_key=body.issue_key,
            jira_worklog_id=worklog_id,
        )
    )
    try:
        session.flush()
    except IntegrityError:
        # Two retries of the same session raced and both got past the SELECT
        # above. The unique constraint caught the second one. Jira now holds a
        # duplicate work log, which is regrettable but not something this request
        # can undo — what it CAN do is not compound it by failing the client into
        # a third attempt. Report the id we just created and move on.
        session.rollback()
        existing = session.scalar(
            select(PushedWorklog).where(
                PushedWorklog.user_id == user.id,
                PushedWorklog.idempotency_key == body.idempotency_key,
            )
        )
        if existing is not None:
            return WorklogResponse(
                jira_worklog_id=existing.jira_worklog_id,
                issue_key=existing.issue_key,
                duplicate=True,
            )
        raise

    return WorklogResponse(jira_worklog_id=worklog_id, issue_key=body.issue_key, duplicate=False)
