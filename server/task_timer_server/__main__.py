"""Entry point for the systemd unit: `task-timer-server`."""

from __future__ import annotations

import uvicorn

from .config import get_settings


def main() -> None:
    settings = get_settings()
    uvicorn.run(
        "task_timer_server.main:app",
        host=settings.host,
        port=settings.port,
        # No reload, no workers>1. One process keeps the httpx pool and the SQLite
        # WAL in one place; scale by putting a reverse proxy in front of several
        # instances backed by Postgres, not by forking this one.
        log_config=None,
    )


if __name__ == "__main__":
    main()
