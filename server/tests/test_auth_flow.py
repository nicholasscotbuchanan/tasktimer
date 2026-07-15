"""The one flow that registers, authenticates, and authorizes Jira, all at once."""

from __future__ import annotations

import secrets
from urllib.parse import parse_qs, urlparse

import httpx
import respx

from task_timer_server.jira import oauth
from task_timer_server.security import pkce_challenge

REDIRECT = "http://127.0.0.1:53682/callback"


def _mock_atlassian(account_id="acct-1", email="alice@acme.com", name="Alice"):
    respx.post(oauth.TOKEN_URL).mock(
        return_value=httpx.Response(
            200,
            json={
                "access_token": "at-1",
                "refresh_token": "rt-1",
                "expires_in": 3600,
            },
        )
    )
    respx.get(oauth.ME_URL).mock(
        return_value=httpx.Response(
            200, json={"account_id": account_id, "email": email, "name": name}
        )
    )
    respx.get(oauth.RESOURCES_URL).mock(
        return_value=httpx.Response(
            200, json=[{"id": "cloud-123", "url": "https://acme.atlassian.net"}]
        )
    )


def _begin(client, verifier: str) -> str:
    """Kick off a login and return the state Atlassian would echo back."""
    resp = client.get(
        "/auth/login",
        params={
            "redirect_uri": REDIRECT,
            "code_challenge": pkce_challenge(verifier),
            "state": "client-state",
        },
        follow_redirects=False,
    )
    assert resp.status_code == 307
    location = resp.headers["location"]
    assert location.startswith(oauth.AUTHORIZE_URL)

    query = parse_qs(urlparse(location).query)
    # offline_access is what makes a refresh token exist at all; without it the
    # grant dies in an hour and the user is asked to consent again every morning.
    assert "offline_access" in query["scope"][0]
    return query["state"][0]


@respx.mock
def test_first_consent_registers_the_user_and_issues_a_key(client):
    _mock_atlassian()
    verifier = secrets.token_urlsafe(48)
    state = _begin(client, verifier)

    # Atlassian sends the browser back to us.
    resp = client.get(
        "/auth/callback", params={"code": "jira-code", "state": state}, follow_redirects=False
    )
    assert resp.status_code == 303
    bounced = urlparse(resp.headers["location"])
    assert f"{bounced.scheme}://{bounced.netloc}{bounced.path}" == REDIRECT

    params = parse_qs(bounced.query)
    assert params["state"] == ["client-state"]
    one_time_code = params["code"][0]

    # The key is NOT in that URL. It comes back in a body, over TLS, and only to
    # a caller that can prove it holds the PKCE verifier.
    assert "api_key" not in bounced.query
    assert "tt_" not in resp.headers["location"]

    resp = client.post(
        "/api/v1/auth/exchange", json={"code": one_time_code, "code_verifier": verifier}
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()

    assert body["api_key"].startswith("tt_")
    assert body["user"]["email"] == "alice@acme.com"
    assert body["user"]["jira_connected"] is True

    # And the key works. Nobody provisioned anything.
    me = client.get("/api/v1/me", headers={"Authorization": f"Bearer {body['api_key']}"})
    assert me.status_code == 200
    assert me.json()["display_name"] == "Alice"


@respx.mock
def test_a_wrong_pkce_verifier_is_refused(client):
    _mock_atlassian()
    state = _begin(client, secrets.token_urlsafe(48))
    resp = client.get(
        "/auth/callback", params={"code": "c", "state": state}, follow_redirects=False
    )
    code = parse_qs(urlparse(resp.headers["location"]).query)["code"][0]

    # A local process that raced to the loopback port and grabbed the code cannot
    # redeem it: it never had the verifier.
    resp = client.post(
        "/api/v1/auth/exchange",
        json={"code": code, "code_verifier": secrets.token_urlsafe(48)},
    )
    assert resp.status_code == 400
    assert "PKCE" in resp.json()["detail"]


@respx.mock
def test_a_code_can_be_redeemed_only_once(client):
    _mock_atlassian()
    verifier = secrets.token_urlsafe(48)
    state = _begin(client, verifier)
    resp = client.get(
        "/auth/callback", params={"code": "c", "state": state}, follow_redirects=False
    )
    code = parse_qs(urlparse(resp.headers["location"]).query)["code"][0]

    first = client.post(
        "/api/v1/auth/exchange", json={"code": code, "code_verifier": verifier}
    )
    assert first.status_code == 200

    second = client.post(
        "/api/v1/auth/exchange", json={"code": code, "code_verifier": verifier}
    )
    assert second.status_code == 400


def test_a_non_loopback_redirect_is_refused(client):
    # Otherwise this endpoint is an open redirect that hands a redeemable code to
    # whoever asks for one.
    resp = client.get(
        "/auth/login",
        params={
            "redirect_uri": "https://evil.example/steal",
            "code_challenge": pkce_challenge(secrets.token_urlsafe(48)),
        },
        follow_redirects=False,
    )
    assert resp.status_code == 400
    assert "loopback" in resp.json()["detail"]


@respx.mock
def test_a_second_consent_reuses_the_account_rather_than_duplicating_it(client, session_factory):
    from task_timer_server.models import User

    _mock_atlassian()
    for _ in range(2):
        verifier = secrets.token_urlsafe(48)
        state = _begin(client, verifier)
        resp = client.get(
            "/auth/callback", params={"code": "c", "state": state}, follow_redirects=False
        )
        code = parse_qs(urlparse(resp.headers["location"]).query)["code"][0]
        client.post("/api/v1/auth/exchange", json={"code": code, "code_verifier": verifier})

    with session_factory() as s:
        assert s.query(User).count() == 1


def test_endpoints_are_closed_without_a_key(client):
    assert client.get("/api/v1/me").status_code == 401
    assert client.get("/api/v1/tasks").status_code == 401
    assert client.post("/api/v1/worklogs", json={}).status_code == 401


def test_a_bogus_key_is_refused(client):
    resp = client.get("/api/v1/me", headers={"Authorization": "Bearer tt_nope"})
    assert resp.status_code == 401
