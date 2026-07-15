"""Jira Cloud REST API v3, as a per-user client.

This is a port of the desktop app's old Go provider (internal/sync/providers/jira).
The transport changed — 3LO bearer tokens against api.atlassian.com/ex/jira/<cloud_id>,
where the Go code used HTTP Basic against the site's own hostname — but every
piece of Jira pedantry the Go code had learned the hard way is preserved here,
because Jira has not become any less fussy:

  - `started` on a work log must be `2024-03-01T12:34:56.789+0000`: exactly three
    fractional digits and an offset with NO colon. RFC 3339 is rejected outright.
  - A comment must be an Atlassian Document Format document, never a plain string
    — and ADF containing an empty text node is *also* rejected, which is why an
    empty comment omits the field rather than sending an empty document.
  - Jira will not accept a work log shorter than a minute. A 40-second session the
    user deliberately timed is rounded up, not silently discarded.
  - "Is this issue done?" is `statusCategory.key == "done"`. The status *name* is
    per-project and means nothing across boards.
  - Transition ids are per-workflow and not stable. The done transition is resolved
    by name on every call, matching either the transition's name or the name of the
    status it leads to, because boards differ on which of the two is "Done".
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta
from urllib.parse import quote, urlencode

import httpx

API_BASE = "https://api.atlassian.com/ex/jira"

TIMEOUT = httpx.Timeout(30.0)
PAGE_SIZE = 100

# The minimum set of fields a task listing needs. Asking for only these keeps the
# response small on a large board.
PULL_FIELDS = "summary,status,assignee,updated,reporter"

# Jira's floor for a work log.
MIN_WORKLOG_SECONDS = 60

MAX_ERR_BODY = 512


class JiraError(RuntimeError):
    """Jira refused a request. The message carries Jira's own explanation."""

    def __init__(self, message: str, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


@dataclass(slots=True)
class RemoteTask:
    key: str
    title: str
    url: str
    status: str
    assigned_by: str
    done: bool
    updated_at: datetime | None


@dataclass(slots=True)
class WorkLog:
    issue_key: str
    started: datetime
    duration_seconds: int
    comment: str = ""


def format_started(when: datetime) -> str:
    """Render a work log's `started` the one way Jira accepts.

    strftime cannot do this: %z gives '+0000' (good) but there is no directive for
    exactly-three fractional digits — %f is six. So the milliseconds are cut by
    hand. Jira rejects both six digits and an offset containing a colon, which is
    precisely why time.RFC3339 was unusable in the Go implementation too.
    """
    if when.tzinfo is None:
        when = when.astimezone()
    millis = when.microsecond // 1000
    return f"{when.strftime('%Y-%m-%dT%H:%M:%S')}.{millis:03d}{when.strftime('%z')}"


def worklog_seconds(duration: timedelta | int) -> int:
    """Whole seconds, rounded up to Jira's one-minute floor."""
    secs = int(duration.total_seconds()) if isinstance(duration, timedelta) else int(duration)
    return max(secs, MIN_WORKLOG_SECONDS)


def plain_adf(text: str) -> dict:
    """Wrap one line of text as a single-paragraph ADF document."""
    return {
        "type": "doc",
        "version": 1,
        "content": [{"type": "paragraph", "content": [{"type": "text", "text": text}]}],
    }


def parse_jira_time(raw: str) -> datetime | None:
    """Parse Jira's colon-less-offset timestamps.

    An unparseable value yields None rather than failing the whole listing: one
    odd `updated` field is not a reason to drop ninety-nine good issues.
    """
    if not raw:
        return None
    for layout in ("%Y-%m-%dT%H:%M:%S.%f%z", "%Y-%m-%dT%H:%M:%S%z"):
        try:
            return datetime.strptime(raw, layout)
        except ValueError:
            continue
    try:
        return datetime.fromisoformat(raw)
    except ValueError:
        return None


def incremental_jql(jql: str, since: datetime | None) -> str:
    """Narrow the configured query to issues touched since the last look.

    The user's JQL is parenthesised so a top-level OR inside it cannot swallow the
    added clause. Jira compares `updated` at minute precision only, so the cursor
    is deliberately coarse — a little overlap is harmless because the client's
    upserts are idempotent.
    """
    if since is None:
        return jql
    return f'({jql}) AND updated >= "{since.astimezone().strftime("%Y/%m/%d %H:%M")}"'


class JiraClient:
    """Talks to one Jira site as one user."""

    def __init__(self, http: httpx.AsyncClient, access_token: str, cloud_id: str, site_url: str = ""):
        self._http = http
        self._token = access_token
        self._cloud_id = cloud_id
        self._site_url = site_url.rstrip("/")

    # -- issues -------------------------------------------------------------

    async def search(self, jql: str, since: datetime | None = None) -> list[RemoteTask]:
        """Every issue matching the JQL, following Jira's cursor pagination.

        /search/jql is token-paginated: there is no startAt, and the end of the
        results is marked by isLast or by the absence of a nextPageToken.
        """
        query = incremental_jql(jql, since)
        tasks: list[RemoteTask] = []
        token = ""
        seen: set[str] = set()

        while True:
            params = {"jql": query, "fields": PULL_FIELDS, "maxResults": str(PAGE_SIZE)}
            if token:
                params["nextPageToken"] = token

            page = await self._request("GET", f"/rest/api/3/search/jql?{urlencode(params)}")

            for issue in page.get("issues", []):
                tasks.append(self._to_remote(issue))

            next_token = page.get("nextPageToken") or ""
            if page.get("isLast") or not next_token:
                return tasks

            # A server that kept handing back the same cursor would spin here
            # forever, hammering Jira and growing the list until we were killed.
            if next_token in seen:
                raise JiraError(
                    f"Jira returned the same nextPageToken {next_token!r} twice; "
                    "giving up rather than paging forever."
                )
            seen.add(next_token)
            token = next_token

    def _to_remote(self, issue: dict) -> RemoteTask:
        fields = issue.get("fields", {}) or {}
        status = fields.get("status") or {}
        category = status.get("statusCategory") or {}
        reporter = fields.get("reporter") or {}

        key = issue.get("key", "")
        return RemoteTask(
            key=key,
            title=fields.get("summary", ""),
            url=f"{self._site_url}/browse/{key}" if self._site_url else "",
            status=status.get("name", ""),
            assigned_by=reporter.get("displayName", ""),
            # The coarse category, never the status name: only this is comparable
            # across projects with different workflows.
            done=str(category.get("key", "")).lower() == "done",
            updated_at=parse_jira_time(fields.get("updated", "")),
        )

    # -- work logs ----------------------------------------------------------

    async def add_worklog(self, wl: WorkLog) -> str:
        """Record a session on the issue; returns Jira's id for the work log."""
        if not wl.issue_key.strip():
            raise JiraError("cannot push a work log without an issue key")

        body: dict = {
            "started": format_started(wl.started),
            "timeSpentSeconds": worklog_seconds(wl.duration_seconds),
        }
        # An ADF document with an empty text node is rejected, so an empty comment
        # means no comment field at all rather than an empty one.
        comment = wl.comment.strip()
        if comment:
            body["comment"] = plain_adf(comment)

        created = await self._request(
            "POST", f"/rest/api/3/issue/{quote(wl.issue_key, safe='')}/worklog", json=body
        )

        worklog_id = created.get("id", "")
        if not worklog_id:
            raise JiraError(
                f"Jira accepted the work log on {wl.issue_key} but returned no work log id"
            )
        return str(worklog_id)

    # -- transitions --------------------------------------------------------

    async def complete(self, issue_key: str, done_transition: str) -> None:
        """Drive the issue through its done transition."""
        if not issue_key.strip():
            raise JiraError("cannot complete an issue without a key")

        path = f"/rest/api/3/issue/{quote(issue_key, safe='')}/transitions"
        available = (await self._request("GET", path)).get("transitions", [])

        transition_id = _find_transition(available, done_transition)
        if transition_id is None:
            raise JiraError(
                f"No transition matching {done_transition!r} is available on {issue_key}. "
                f"Available transitions: {_describe_transitions(available)}"
            )

        await self._request("POST", path, json={"transition": {"id": transition_id}})

    # -- http ---------------------------------------------------------------

    async def _request(self, method: str, path: str, json: dict | None = None) -> dict:
        url = f"{API_BASE}/{self._cloud_id}{path}"
        headers = {"Authorization": f"Bearer {self._token}", "Accept": "application/json"}
        if json is not None:
            headers["Content-Type"] = "application/json"

        resp = await self._http.request(method, url, headers=headers, json=json, timeout=TIMEOUT)

        if not resp.is_success:
            raise _api_error(method, url, resp)

        if not resp.content:
            return {}
        try:
            body = resp.json()
        except ValueError:
            return {}
        return body if isinstance(body, dict) else {"result": body}


def _find_transition(transitions: list[dict], want: str) -> str | None:
    """Match on the transition's own name, or on the status it leads to.

    Boards vary: some name the transition "Done", others name it "Finish work" and
    only the destination status is "Done". Accepting either spares the user from
    having to know which kind of board they are on.
    """
    target = want.strip().casefold()
    for t in transitions:
        name = str(t.get("name", "")).strip().casefold()
        to_name = str((t.get("to") or {}).get("name", "")).strip().casefold()
        if target in (name, to_name):
            return str(t.get("id"))
    return None


def _describe_transitions(transitions: list[dict]) -> str:
    """Render the available transitions for an error message.

    A missing transition is the single most common Jira misconfiguration there is,
    and the fix is always "use one of these names" — so the names go in the error.
    """
    if not transitions:
        return "(none — the issue may already be closed, or the account may lack permission to transition it)"
    parts = []
    for t in transitions:
        name = str(t.get("name", ""))
        to_name = str((t.get("to") or {}).get("name", ""))
        if to_name and to_name.casefold() != name.casefold():
            parts.append(f"{name!r} (to {to_name!r})")
        else:
            parts.append(repr(name))
    return ", ".join(parts)


def _api_error(method: str, url: str, resp: httpx.Response) -> JiraError:
    """Turn a non-2xx into an error that says what Jira actually objected to.

    Jira answers a bad work log with a 400 whose body explains exactly why.
    Discarding that in favour of "unexpected status 400" throws away the only
    useful part of the response.
    """
    detail = ""
    try:
        body = resp.json()
        parts = list(body.get("errorMessages", []))
        parts += [f"{field}: {msg}" for field, msg in (body.get("errors") or {}).items()]
        detail = "; ".join(parts)[:MAX_ERR_BODY]
    except ValueError:
        detail = resp.text.strip()[:MAX_ERR_BODY]

    where = _redact(url)
    message = f"{method} {where}: Jira returned {resp.status_code}"
    if detail:
        message = f"{message}: {detail}"
    return JiraError(message, status_code=resp.status_code)


def _redact(url: str) -> str:
    """Strip the query string before a URL goes into an error or a log line.

    The JQL in a search URL is long, noisy, and can name people.
    """
    return url.split("?", 1)[0]
