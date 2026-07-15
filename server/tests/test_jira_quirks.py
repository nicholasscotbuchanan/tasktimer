"""The Jira pedantry the Go provider learned the hard way.

Every one of these is a rule Jira enforces and will reject a request over. They
are tested at this level, rather than through the API, because that is where the
old implementation's knowledge lived and this is the port of it.
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import httpx
import pytest
import respx

from task_timer_server.jira.client import (
    API_BASE,
    JiraClient,
    JiraError,
    WorkLog,
    format_started,
    incremental_jql,
    parse_jira_time,
    plain_adf,
    worklog_seconds,
)

CLOUD = "cloud-123"
BASE = f"{API_BASE}/{CLOUD}"


# --- started -----------------------------------------------------------------


def test_started_has_exactly_three_millis_and_no_colon_in_offset():
    when = datetime(2024, 3, 1, 12, 34, 56, 789_123, tzinfo=timezone.utc)
    assert format_started(when) == "2024-03-01T12:34:56.789+0000"


def test_started_pads_millis():
    when = datetime(2024, 3, 1, 12, 34, 56, 7_000, tzinfo=timezone.utc)
    # .007, not .7 — Jira wants three digits, always.
    assert format_started(when) == "2024-03-01T12:34:56.007+0000"


def test_started_keeps_a_non_utc_offset_colonless():
    tz = timezone(timedelta(hours=-5))
    when = datetime(2024, 3, 1, 12, 0, 0, 0, tzinfo=tz)
    assert format_started(when) == "2024-03-01T12:00:00.000-0500"


def test_started_is_not_rfc3339():
    # The whole point: RFC 3339 renders '+00:00' and six fractional digits, and
    # Jira rejects both. If this ever starts passing, the format has regressed.
    when = datetime(2024, 3, 1, 12, 34, 56, 789_000, tzinfo=timezone.utc)
    assert format_started(when) != when.isoformat()


# --- the 60-second floor -----------------------------------------------------


@pytest.mark.parametrize(
    "seconds,expected",
    [(40, 60), (1, 60), (59, 60), (60, 60), (61, 61), (1500, 1500)],
)
def test_short_sessions_round_up_rather_than_vanish(seconds, expected):
    assert worklog_seconds(seconds) == expected


def test_worklog_seconds_accepts_a_timedelta():
    assert worklog_seconds(timedelta(minutes=25)) == 1500


# --- ADF ---------------------------------------------------------------------


def test_comment_is_an_adf_document_not_a_string():
    doc = plain_adf("traced the drift")
    assert doc["type"] == "doc"
    assert doc["version"] == 1
    assert doc["content"][0]["content"][0]["text"] == "traced the drift"


@respx.mock
async def test_empty_comment_omits_the_field_entirely():
    # ADF with an empty text node is rejected by Jira, so the field must be absent
    # rather than present-and-empty.
    route = respx.post(f"{BASE}/rest/api/3/issue/ENG-1/worklog").mock(
        return_value=httpx.Response(201, json={"id": "10001"})
    )
    async with httpx.AsyncClient() as http:
        jira = JiraClient(http, "tok", CLOUD)
        await jira.add_worklog(
            WorkLog(
                issue_key="ENG-1",
                started=datetime(2024, 3, 1, tzinfo=timezone.utc),
                duration_seconds=300,
                comment="   ",
            )
        )
    body = respx.calls.last.request.read().decode()
    assert "comment" not in body
    assert route.called


@respx.mock
async def test_worklog_sends_the_jira_shape():
    respx.post(f"{BASE}/rest/api/3/issue/ENG-412/worklog").mock(
        return_value=httpx.Response(201, json={"id": "99"})
    )
    async with httpx.AsyncClient() as http:
        jira = JiraClient(http, "tok", CLOUD)
        got = await jira.add_worklog(
            WorkLog(
                issue_key="ENG-412",
                started=datetime(2024, 3, 1, 9, 15, 0, tzinfo=timezone.utc),
                duration_seconds=30,
                comment="hi",
            )
        )
    assert got == "99"
    import json as _json

    body = _json.loads(respx.calls.last.request.read())
    assert body["started"] == "2024-03-01T09:15:00.000+0000"
    assert body["timeSpentSeconds"] == 60  # rounded up off 30
    assert body["comment"]["type"] == "doc"


# --- done is a statusCategory, never a name ----------------------------------


@respx.mock
async def test_done_comes_from_status_category_not_the_status_name():
    respx.get(url__startswith=f"{BASE}/rest/api/3/search/jql").mock(
        return_value=httpx.Response(
            200,
            json={
                "isLast": True,
                "issues": [
                    {
                        "key": "ENG-1",
                        "fields": {
                            "summary": "shipped",
                            # A board whose 'done' status is called something else
                            # entirely. The name tells us nothing; the category does.
                            "status": {
                                "name": "Released to prod",
                                "statusCategory": {"key": "done"},
                            },
                            "updated": "2024-03-01T12:34:56.789+0000",
                            "reporter": {"displayName": "Dana"},
                        },
                    },
                    {
                        "key": "ENG-2",
                        "fields": {
                            # ...and a status literally named "Done" that is NOT done.
                            "status": {
                                "name": "Done pending review",
                                "statusCategory": {"key": "indeterminate"},
                            },
                        },
                    },
                ],
            },
        )
    )
    async with httpx.AsyncClient() as http:
        tasks = await JiraClient(http, "tok", CLOUD, "https://acme.atlassian.net").search("x")

    assert [(t.key, t.done) for t in tasks] == [("ENG-1", True), ("ENG-2", False)]
    assert tasks[0].url == "https://acme.atlassian.net/browse/ENG-1"
    assert tasks[0].assigned_by == "Dana"


# --- pagination --------------------------------------------------------------


@respx.mock
async def test_search_follows_the_next_page_token():
    pages = [
        httpx.Response(200, json={"issues": [{"key": "A-1", "fields": {}}], "nextPageToken": "p2"}),
        httpx.Response(200, json={"issues": [{"key": "A-2", "fields": {}}], "isLast": True}),
    ]
    respx.get(url__startswith=f"{BASE}/rest/api/3/search/jql").mock(side_effect=pages)
    async with httpx.AsyncClient() as http:
        tasks = await JiraClient(http, "tok", CLOUD).search("x")
    assert [t.key for t in tasks] == ["A-1", "A-2"]


@respx.mock
async def test_a_repeated_cursor_is_refused_rather_than_paged_forever():
    respx.get(url__startswith=f"{BASE}/rest/api/3/search/jql").mock(
        return_value=httpx.Response(
            200, json={"issues": [{"key": "A-1", "fields": {}}], "nextPageToken": "same"}
        )
    )
    async with httpx.AsyncClient() as http:
        with pytest.raises(JiraError, match="same nextPageToken"):
            await JiraClient(http, "tok", CLOUD).search("x")


# --- JQL ---------------------------------------------------------------------


def test_incremental_jql_parenthesises_the_users_query():
    # Without the parens, a top-level OR in the user's JQL swallows the added
    # clause and the 'since' window silently does nothing.
    got = incremental_jql(
        "assignee = currentUser() OR reporter = currentUser()",
        datetime(2024, 3, 1, 9, 30, tzinfo=timezone.utc).astimezone(),
    )
    assert got.startswith("(assignee = currentUser() OR reporter = currentUser()) AND updated >= ")


def test_incremental_jql_without_a_cursor_is_untouched():
    assert incremental_jql("assignee = currentUser()", None) == "assignee = currentUser()"


# --- timestamps in ------------------------------------------------------------


def test_parses_jiras_colonless_offset():
    got = parse_jira_time("2024-03-01T12:34:56.789+0000")
    assert got == datetime(2024, 3, 1, 12, 34, 56, 789_000, tzinfo=timezone.utc)


def test_an_unparseable_timestamp_yields_none_rather_than_exploding():
    # One odd `updated` value must not drop ninety-nine good issues.
    assert parse_jira_time("last tuesday") is None


# --- transitions --------------------------------------------------------------


@respx.mock
async def test_complete_matches_the_destination_status_when_the_transition_is_named_otherwise():
    respx.get(f"{BASE}/rest/api/3/issue/ENG-9/transitions").mock(
        return_value=httpx.Response(
            200,
            json={
                "transitions": [
                    {"id": "11", "name": "Start work", "to": {"name": "In Progress"}},
                    # The transition is called "Finish work"; only the status it
                    # leads to is "Done". Matching on either is what lets one
                    # config work across boards that disagree.
                    {"id": "31", "name": "Finish work", "to": {"name": "Done"}},
                ]
            },
        )
    )
    posted = respx.post(f"{BASE}/rest/api/3/issue/ENG-9/transitions").mock(
        return_value=httpx.Response(204)
    )
    async with httpx.AsyncClient() as http:
        await JiraClient(http, "tok", CLOUD).complete("ENG-9", "Done")

    import json as _json

    assert _json.loads(posted.calls.last.request.read()) == {"transition": {"id": "31"}}


@respx.mock
async def test_a_missing_transition_names_the_ones_that_exist():
    respx.get(f"{BASE}/rest/api/3/issue/ENG-9/transitions").mock(
        return_value=httpx.Response(
            200, json={"transitions": [{"id": "11", "name": "Start work", "to": {"name": "In Progress"}}]}
        )
    )
    async with httpx.AsyncClient() as http:
        with pytest.raises(JiraError) as exc:
            await JiraClient(http, "tok", CLOUD).complete("ENG-9", "Done")
    # The fix is always "use one of these names", so the names belong in the error.
    assert "Start work" in str(exc.value)


# --- errors -------------------------------------------------------------------


@respx.mock
async def test_jiras_own_complaint_survives_into_the_error():
    respx.post(f"{BASE}/rest/api/3/issue/ENG-1/worklog").mock(
        return_value=httpx.Response(
            400, json={"errorMessages": [], "errors": {"timeSpentSeconds": "must be positive"}}
        )
    )
    async with httpx.AsyncClient() as http:
        with pytest.raises(JiraError) as exc:
            await JiraClient(http, "tok", CLOUD).add_worklog(
                WorkLog("ENG-1", datetime.now(timezone.utc), 300)
            )
    assert "timeSpentSeconds: must be positive" in str(exc.value)


@respx.mock
async def test_the_jql_is_stripped_out_of_error_messages():
    # A search URL's query string is long, noisy, and can name people.
    respx.get(url__startswith=f"{BASE}/rest/api/3/search/jql").mock(
        return_value=httpx.Response(500, text="boom")
    )
    async with httpx.AsyncClient() as http:
        with pytest.raises(JiraError) as exc:
            await JiraClient(http, "tok", CLOUD).search("assignee = 'alice@example.com'")
    assert "alice@example.com" not in str(exc.value)
    assert "?" not in str(exc.value)
