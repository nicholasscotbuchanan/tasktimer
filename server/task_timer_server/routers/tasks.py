"""The task list, and closing an issue.

Replaces the old daemon's Pull and Complete. The JQL is the server's, not the
client's: a client that could send arbitrary JQL could read any issue the user
can see, which is a larger surface than a timer needs and is not something the
desktop app was ever able to do anyway.
"""

from __future__ import annotations

from datetime import datetime

from fastapi import APIRouter, Depends, HTTPException, Query, status
from sqlalchemy.orm import Session

from ..config import Settings, get_settings
from ..db import get_session
from ..jira import JiraError
from ..models import User
from ..schemas import CompleteResponse, Task, TaskList
from ..security import current_user
from ..service import jira_for

router = APIRouter(prefix="/api/v1", tags=["tasks"])


@router.get(
    "/tasks",
    response_model=TaskList,
    summary="The issues assigned to you",
    description="Runs the server's configured JQL as *you*, via your own Atlassian grant. "
    "Pass `since` to fetch only what changed — Jira compares `updated` at minute "
    "precision, so the window is deliberately coarse and a little overlap is normal.",
)
async def list_tasks(
    since: datetime | None = Query(default=None, description="Only issues updated at or after this."),
    user: User = Depends(current_user),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> TaskList:
    client = await jira_for(session, user, settings)
    try:
        remotes = await client.search(settings.jira_jql, since)
    except JiraError as exc:
        raise _upstream(exc) from exc

    return TaskList(tasks=[Task(**vars(r)) for r in remotes])


@router.post(
    "/tasks/{issue_key}/complete",
    response_model=CompleteResponse,
    summary="Transition an issue to done",
    description="Disabled unless the server sets `jira.allow_complete`. Writing to a shared "
    "board is opt-in — the same stance the desktop daemon took.",
)
async def complete_task(
    issue_key: str,
    user: User = Depends(current_user),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> CompleteResponse:
    if not settings.jira_allow_complete:
        raise HTTPException(
            status_code=status.HTTP_403_FORBIDDEN,
            detail="This server does not allow clients to close Jira issues. "
            "An administrator can enable it with jira.allow_complete.",
        )

    client = await jira_for(session, user, settings)
    try:
        await client.complete(issue_key, settings.jira_done_transition)
    except JiraError as exc:
        raise _upstream(exc) from exc

    return CompleteResponse(issue_key=issue_key, transitioned=True)


def _upstream(exc: JiraError) -> HTTPException:
    """Map a Jira failure onto a status the client can actually act on.

    A 401/403 from Jira is not the client's fault and must not be reported as
    one: answering with 401 would make the desktop app throw away a perfectly
    good API key and demand the user log in to the *backend* again, when the real
    problem is upstream. 502 says plainly that the fault is between us and Jira.
    """
    if exc.status_code == 404:
        return HTTPException(status_code=404, detail=str(exc))
    if exc.status_code in (401, 403):
        return HTTPException(
            status_code=status.HTTP_502_BAD_GATEWAY,
            detail=f"Jira refused the request for this account: {exc}",
        )
    return HTTPException(status_code=status.HTTP_502_BAD_GATEWAY, detail=str(exc))
