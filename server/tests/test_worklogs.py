"""Pushing sessions: attribution, and not logging the same session twice."""

from __future__ import annotations

import secrets
from urllib.parse import parse_qs, urlparse

import httpx
import pytest
import respx

from task_timer_server.jira import oauth
from task_timer_server.jira.client import API_BASE
from task_timer_server.security import pkce_challenge

BASE = f"{API_BASE}/cloud-123"
REDIRECT = "http://127.0.0.1:53682/callback"


@pytest.fixture
def api_key(client):
    """Register a user the way a real one registers: by consenting."""
    with respx.mock:
        respx.post(oauth.TOKEN_URL).mock(
            return_value=httpx.Response(
                200, json={"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600}
            )
        )
        respx.get(oauth.ME_URL).mock(
            return_value=httpx.Response(
                200, json={"account_id": "acct-1", "email": "alice@acme.com", "name": "Alice"}
            )
        )
        respx.get(oauth.RESOURCES_URL).mock(
            return_value=httpx.Response(
                200, json=[{"id": "cloud-123", "url": "https://acme.atlassian.net"}]
            )
        )

        verifier = secrets.token_urlsafe(48)
        resp = client.get(
            "/auth/login",
            params={"redirect_uri": REDIRECT, "code_challenge": pkce_challenge(verifier)},
            follow_redirects=False,
        )
        state = parse_qs(urlparse(resp.headers["location"]).query)["state"][0]
        resp = client.get(
            "/auth/callback", params={"code": "c", "state": state}, follow_redirects=False
        )
        code = parse_qs(urlparse(resp.headers["location"]).query)["code"][0]
        resp = client.post(
            "/api/v1/auth/exchange", json={"code": code, "code_verifier": verifier}
        )
        return resp.json()["api_key"]


def auth(key):
    return {"Authorization": f"Bearer {key}"}


SESSION = {
    "issue_key": "ENG-412",
    "started": "2024-03-01T09:15:00+00:00",
    "duration_seconds": 1500,
    "comment": "traced the drift",
    "idempotency_key": "7f3c1e0a-2b44-4d9e-9f16-0a1b2c3d4e5f",
}


@respx.mock
def test_a_session_is_written_to_jira_as_the_user(client, api_key):
    route = respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        return_value=httpx.Response(201, json={"id": "10001"})
    )

    resp = client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))
    assert resp.status_code == 201, resp.text
    assert resp.json() == {
        "jira_worklog_id": "10001",
        "issue_key": "ENG-412",
        "duplicate": False,
    }

    # Alice's own access token, not a shared bot's. This is the whole reason the
    # backend does per-user OAuth: Jira attributes the work log to whoever
    # authenticated the call, so a service account would put every person's time
    # under one name.
    assert route.calls.last.request.headers["authorization"] == "Bearer at-1"


@respx.mock
def test_a_retry_with_the_same_key_does_not_log_the_session_twice(client, api_key):
    route = respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        return_value=httpx.Response(201, json={"id": "10001"})
    )

    first = client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))
    second = client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))

    assert first.json()["duplicate"] is False
    assert second.json()["duplicate"] is True
    assert second.json()["jira_worklog_id"] == "10001"

    # The important assertion in this file. Jira has no idempotency of its own —
    # it will happily accept the same work log five times — so if this is ever
    # 2, the client's retry has silently double-billed someone's day.
    assert route.call_count == 1


@respx.mock
def test_a_different_session_still_gets_through(client, api_key):
    route = respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        side_effect=[
            httpx.Response(201, json={"id": "10001"}),
            httpx.Response(201, json={"id": "10002"}),
        ]
    )
    client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))
    other = client.post(
        "/api/v1/worklogs",
        json={**SESSION, "idempotency_key": "a-different-session-entirely"},
        headers=auth(api_key),
    )
    assert other.json()["jira_worklog_id"] == "10002"
    assert route.call_count == 2


@respx.mock
def test_an_expired_grant_is_refreshed_and_the_rotated_token_is_kept(client, api_key, session_factory):
    from datetime import datetime, timedelta, timezone

    from task_timer_server.models import JiraToken
    from task_timer_server.security import decrypt

    # Age the access token out.
    with session_factory() as s:
        token = s.query(JiraToken).one()
        token.expires_at = datetime.now(timezone.utc) - timedelta(minutes=5)
        s.commit()

    respx.post(oauth.TOKEN_URL).mock(
        return_value=httpx.Response(
            200, json={"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600}
        )
    )
    route = respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        return_value=httpx.Response(201, json={"id": "10001"})
    )

    resp = client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))
    assert resp.status_code == 201

    # The refreshed token was used...
    assert route.calls.last.request.headers["authorization"] == "Bearer at-2"

    # ...and, critically, the ROTATED refresh token was persisted. Atlassian kills
    # rt-1 the instant it issues rt-2. If we kept rt-1, the next refresh would
    # fail and the user would be locked out with no way to tell why.
    with session_factory() as s:
        token = s.query(JiraToken).one()
        assert decrypt(token.refresh_token_enc) == "rt-2"


@respx.mock
def test_jira_refusing_us_is_not_reported_as_the_clients_fault(client, api_key):
    respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        return_value=httpx.Response(403, json={"errorMessages": ["no permission on this board"]})
    )
    resp = client.post("/api/v1/worklogs", json=SESSION, headers=auth(api_key))

    # 502, not 401. A 401 would make the desktop client throw away a perfectly
    # good API key and march the user through a login that fixes nothing.
    assert resp.status_code == 502
    assert "no permission" in resp.json()["detail"]


@respx.mock
def test_tokens_are_encrypted_at_rest(session_factory, api_key):
    from task_timer_server.models import JiraToken

    with session_factory() as s:
        token = s.query(JiraToken).one()
        # Whatever is in that column, it is not the token.
        assert "rt-1" not in token.refresh_token_enc
        assert "at-1" not in token.access_token_enc
