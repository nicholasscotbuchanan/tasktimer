"""Server configuration.

Settings come from, in ascending order of precedence:

  1. /etc/task-timer-server/config.toml   (installed by the .deb / .rpm)
  2. environment variables prefixed TASK_TIMER_SERVER_
  3. systemd credentials, for the two secrets that must never sit in a config
     file readable by anyone who can read /etc

The Atlassian client secret and the token-encryption key are deliberately NOT
given defaults. A server that generates its own encryption key on first boot
would quietly invalidate every stored refresh token the next time it restarts,
and every user would have to re-consent without being told why.
"""

from __future__ import annotations

import os
import tomllib
from functools import lru_cache
from pathlib import Path
from typing import Any

from pydantic import Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

CONFIG_PATH = Path(os.environ.get("TASK_TIMER_SERVER_CONFIG", "/etc/task-timer-server/config.toml"))

# systemd's LoadCredential drops secrets here, mode 0400, owned by the service
# user. Preferred over the environment: /proc/<pid>/environ is readable by the
# same user, and an env var leaks into every child process and crash dump.
CREDENTIALS_DIR = os.environ.get("CREDENTIALS_DIRECTORY")


def _read_credential(name: str) -> str | None:
    if not CREDENTIALS_DIR:
        return None
    path = Path(CREDENTIALS_DIR) / name
    if not path.is_file():
        return None
    return path.read_text(encoding="utf-8").strip() or None


def _toml_source() -> dict[str, Any]:
    if not CONFIG_PATH.is_file():
        return {}
    with CONFIG_PATH.open("rb") as fh:
        raw = tomllib.load(fh)
    # Flatten one level of tables: [jira] base_url -> jira_base_url. Keeps the
    # config file readable without inventing a nested settings model.
    flat: dict[str, Any] = {}
    for key, value in raw.items():
        if isinstance(value, dict):
            for sub, subvalue in value.items():
                flat[f"{key}_{sub}"] = subvalue
        else:
            flat[key] = value
    return flat


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="TASK_TIMER_SERVER_", extra="ignore")

    # --- service -----------------------------------------------------------
    host: str = "127.0.0.1"
    port: int = 8080
    public_url: str = Field(
        default="http://127.0.0.1:8080",
        description="The URL browsers reach this server on. Must match the redirect URI "
        "registered with Atlassian, byte for byte.",
    )
    database_url: str = "sqlite:////var/lib/task-timer-server/server.db"

    # --- atlassian oauth 2.0 (3LO) ----------------------------------------
    atlassian_client_id: str = ""
    atlassian_client_secret: str = ""

    # --- jira ---------------------------------------------------------------
    jira_jql: str = "assignee = currentUser() AND statusCategory != Done"
    jira_done_transition: str = "Done"
    jira_allow_complete: bool = Field(
        default=False,
        description="Let clients transition issues to done. Off by default: writing to a "
        "shared board is opt-in, exactly as it was in the old desktop daemon.",
    )

    # --- registration -------------------------------------------------------
    # Empty means "anyone who can consent to our Atlassian app". That is already
    # bounded — a stranger's token carries no access to your Jira site, so they
    # can register but can do nothing. Set this when you want the tighter fence.
    allowed_email_domains: list[str] = Field(default_factory=list)

    # --- secrets ------------------------------------------------------------
    # Fernet key, urlsafe-base64 32 bytes. Encrypts Jira refresh tokens at rest.
    token_encryption_key: str = ""

    @field_validator("public_url")
    @classmethod
    def _strip_trailing_slash(cls, v: str) -> str:
        return v.rstrip("/")

    @field_validator("allowed_email_domains", mode="before")
    @classmethod
    def _split_domains(cls, v: Any) -> Any:
        if isinstance(v, str):
            return [d.strip().lower() for d in v.split(",") if d.strip()]
        return v

    @property
    def redirect_uri(self) -> str:
        return f"{self.public_url}/auth/callback"

    def require_oauth(self) -> None:
        """Fail loudly at startup rather than with a 500 on the first login."""
        missing = [
            name
            for name, value in (
                ("atlassian_client_id", self.atlassian_client_id),
                ("atlassian_client_secret", self.atlassian_client_secret),
                ("token_encryption_key", self.token_encryption_key),
            )
            if not value
        ]
        if missing:
            raise RuntimeError(
                "task-timer-server is not configured: missing "
                + ", ".join(missing)
                + f". Set them in {CONFIG_PATH}, in the environment as "
                + ", ".join(f"TASK_TIMER_SERVER_{m.upper()}" for m in missing)
                + ", or as systemd credentials."
            )


@lru_cache
def get_settings() -> Settings:
    data = _toml_source()

    # Secrets from systemd credentials win over the config file: the whole point
    # of LoadCredential is that they never appear in a file under /etc.
    for field, credential in (
        ("atlassian_client_secret", "atlassian_client_secret"),
        ("token_encryption_key", "token_encryption_key"),
    ):
        value = _read_credential(credential)
        if value:
            data[field] = value

    return Settings(**data)
