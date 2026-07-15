from __future__ import annotations

import os

os.environ["TASK_TIMER_SERVER_CONFIG"] = "/nonexistent/config.toml"
os.environ["TASK_TIMER_SERVER_DATABASE_URL"] = "sqlite://"
os.environ["TASK_TIMER_SERVER_ATLASSIAN_CLIENT_ID"] = "test-client-id"
os.environ["TASK_TIMER_SERVER_ATLASSIAN_CLIENT_SECRET"] = "test-client-secret"
os.environ["TASK_TIMER_SERVER_PUBLIC_URL"] = "https://timer.example.com"
os.environ["TASK_TIMER_SERVER_JIRA_ALLOW_COMPLETE"] = "true"

import pytest
from cryptography.fernet import Fernet
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker
from sqlalchemy.pool import StaticPool

os.environ["TASK_TIMER_SERVER_TOKEN_ENCRYPTION_KEY"] = Fernet.generate_key().decode()

from task_timer_server import db as db_module  # noqa: E402
from task_timer_server.config import get_settings  # noqa: E402
from task_timer_server.models import Base  # noqa: E402


@pytest.fixture
def settings():
    get_settings.cache_clear()
    return get_settings()


@pytest.fixture
def session_factory(settings):
    # One in-memory database shared across every connection for the test's life.
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )
    Base.metadata.create_all(engine)
    factory = sessionmaker(bind=engine, autoflush=False, expire_on_commit=False, future=True)
    db_module.reset_for_tests(engine, factory)
    return factory


@pytest.fixture
def client(session_factory):
    from task_timer_server.main import app

    with TestClient(app) as c:
        yield c


@pytest.fixture
def session(session_factory):
    s = session_factory()
    try:
        yield s
    finally:
        s.close()
