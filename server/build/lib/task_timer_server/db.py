"""Database engine and session plumbing."""

from __future__ import annotations

from collections.abc import Iterator
from pathlib import Path

from sqlalchemy import create_engine, event
from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session, sessionmaker

from .config import get_settings
from .models import Base

_engine: Engine | None = None
_SessionLocal: sessionmaker[Session] | None = None


def _sqlite_path(url: str) -> Path | None:
    prefix = "sqlite:///"
    if not url.startswith(prefix):
        return None
    return Path(url[len(prefix) :])


def init_engine() -> Engine:
    global _engine, _SessionLocal
    if _engine is not None:
        return _engine

    settings = get_settings()
    url = settings.database_url

    connect_args: dict[str, object] = {}
    if url.startswith("sqlite"):
        # The service is a single process serving many clients; a request may
        # touch the session from a threadpool worker.
        connect_args["check_same_thread"] = False
        path = _sqlite_path(url)
        if path is not None:
            path.parent.mkdir(parents=True, exist_ok=True)

    _engine = create_engine(url, connect_args=connect_args, pool_pre_ping=True, future=True)

    if url.startswith("sqlite"):

        @event.listens_for(_engine, "connect")
        def _sqlite_pragmas(dbapi_connection, _record):  # type: ignore[no-untyped-def]
            cur = dbapi_connection.cursor()
            cur.execute("PRAGMA journal_mode=WAL")
            cur.execute("PRAGMA foreign_keys=ON")
            cur.execute("PRAGMA busy_timeout=5000")
            cur.close()

    Base.metadata.create_all(_engine)
    _SessionLocal = sessionmaker(bind=_engine, autoflush=False, expire_on_commit=False, future=True)
    return _engine


def get_session() -> Iterator[Session]:
    """FastAPI dependency: one session per request, committed or rolled back."""
    if _SessionLocal is None:
        init_engine()
    assert _SessionLocal is not None
    session = _SessionLocal()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()


def reset_for_tests(engine: Engine, factory: sessionmaker[Session]) -> None:
    global _engine, _SessionLocal
    _engine = engine
    _SessionLocal = factory
