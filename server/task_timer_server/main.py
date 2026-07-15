"""The FastAPI application."""

from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import FastAPI

from .config import get_settings
from .db import init_engine
from .routers import auth, tasks, worklogs
from .service import close_http_client

DESCRIPTION = """
The Task Timer backend. The desktop client times work locally in SQLite and pushes
completed sessions here; this service writes them to Jira.

**Jira credentials never leave this server.** Each user connects their own Atlassian
account once, over OAuth 2.0 (3LO), and work logs are authored in Jira by *them* —
not by a shared bot account.

### Getting a key

1. The desktop client opens `/auth/login` in a browser.
2. The user consents at Atlassian. If they have never used this server, that consent
   creates their account — there is nothing to provision.
3. Atlassian returns to `/auth/callback`, which bounces a one-time code to the
   client's loopback listener.
4. The client trades that code at `POST /api/v1/auth/exchange` for a bearer key.

Every other endpoint takes `Authorization: Bearer <key>`.
"""


@asynccontextmanager
async def lifespan(_: FastAPI):
    settings = get_settings()
    # Fail at boot, not on the first user's login. A service that starts happily
    # and then 500s on anyone who tries to connect is a service that looks healthy
    # to every monitor you have.
    settings.require_oauth()
    init_engine()
    yield
    await close_http_client()


app = FastAPI(
    title="Task Timer",
    version="1.0.0",
    description=DESCRIPTION,
    lifespan=lifespan,
    openapi_tags=[
        {"name": "auth", "description": "Connecting Jira, registering, and getting a key."},
        {"name": "tasks", "description": "The issues you can time against."},
        {"name": "worklogs", "description": "Pushing timed sessions to Jira."},
    ],
)

app.include_router(auth.router)
app.include_router(tasks.router)
app.include_router(worklogs.router)


@app.get("/healthz", tags=["ops"], summary="Liveness probe")
def healthz() -> dict[str, str]:
    return {"status": "ok"}
