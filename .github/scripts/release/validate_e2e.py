#!/usr/bin/env python3
"""Validate a release-candidate SPX engine against an spxbench e2e suite.

Flow: publish an rc prerelease (the shelf spxbench can download from) ->
resolve the gate suite by name -> register the candidate version on spxbench
via sourceUrl -> run the suite pinned to that version -> poll to a terminal
state -> report green/red. The rc prerelease is deleted on every exit path.

Required environment:
  SPXBENCH_API_TOKEN  spxbench admin bearer token
  ZIP_PATH            path to the built spx_web.zip
  RC_VERSION          candidate version, e.g. 4.0.2-rc.43192600496.1
Optional:
  SPXBENCH_URL        spxbench base URL (default: https://spxbench.qiniu.io)
  SUITE_NAME          gate suite name (default: spx; cross-repo contract)
  RUN_TIMEOUT_MINUTES overall budget (default: 30)
  GITHUB_REPOSITORY / GITHUB_SHA  provided by Actions; fall back to gh/git
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# Public instance; override with SPXBENCH_URL to point elsewhere. Kept as a
# default (not a secret/variable) so job logs print readable detail links.
DEFAULT_SPXBENCH_URL = "https://spxbench.qiniu.io"

POLL_INTERVAL_SECONDS = 30
BUSY_RETRY_SECONDS = 120


class ValidationError(Exception):
    """Fatal validation-flow error; the message is user-facing log text."""


def log(message):
    # Release-script convention: [info] on stdout, [error] on stderr.
    print(f"[info] {message}", flush=True)


def run(*args, check=True):
    result = subprocess.run(args, text=True, capture_output=True)
    if check and result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise ValidationError(f"command failed ({result.returncode}): {' '.join(args)}"
                              + (f": {detail}" if detail else ""))
    return result


def api(url, token, method="GET", payload=None):
    """Return (http_status, parsed_body) for one spxbench API call."""
    request = urllib.request.Request(url, method=method)
    request.add_header("Authorization", f"Bearer {token}")
    data = None
    if payload is not None:
        request.add_header("Content-Type", "application/json")
        data = json.dumps(payload).encode()
    try:
        with urllib.request.urlopen(request, data=data, timeout=180) as response:
            body = response.read().decode()
            return response.status, (json.loads(body) if body else {})
    except urllib.error.HTTPError as error:
        body = error.read().decode()
        try:
            return error.code, json.loads(body)
        except ValueError:
            return error.code, body


def require_env(name):
    value = os.environ.get(name, "").strip()
    if not value:
        raise ValidationError(f"required environment variable {name} is not set")
    return value


def repository_name():
    env_value = os.environ.get("GITHUB_REPOSITORY", "").strip()
    if env_value:
        return env_value
    return run("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").stdout.strip()


def commit_sha():
    env_value = os.environ.get("GITHUB_SHA", "").strip()
    if env_value:
        return env_value
    return run("git", "rev-parse", "HEAD").stdout.strip()


def find_suite_id(suites, name):
    for suite in suites or []:
        if suite.get("name") == name:
            return suite.get("id")
    return None


def failing_cases(state):
    return [case for case in state.get("cases") or [] if case.get("status") != "pass"]


def job_problems(state):
    """Fatal problems for a finished job; an empty list means the job truly passed.

    The per-case list alone is not the verdict: spxbench also fails a job on
    process-level errors (summary/lastError) even when every case passed.
    """
    problems = []
    summary = (state.get("summary") or "").strip()
    if summary != "passed":
        problems.append(f"job summary is {summary!r}, want 'passed'")
    last_error = (state.get("lastError") or "").strip()
    if last_error:
        problems.append(f"job error: {last_error}")
    total, completed = state.get("total") or 0, state.get("completed") or 0
    if completed != total:
        problems.append(f"job incomplete: completed={completed} of total={total}")
    failures = failing_cases(state)
    if failures:
        problems.append(f"{len(failures)} case(s) did not pass")
    return problems


def is_busy_conflict(body):
    """True only for the recoverable occupied-slot 409; other 409s are permanent."""
    detail = body if isinstance(body, str) else json.dumps(body)
    return "already in progress" in detail


def create_rc_release(rc_tag, zip_path, target):
    log(f"1/7 creating rc prerelease {rc_tag}")
    run("gh", "release", "create", rc_tag, f"{zip_path}#spx_web.zip",
        "--title", f"SPX {rc_tag[1:]} (e2e validation candidate)",
        "--notes", "Temporary candidate uploaded by release validation; deleted when validation ends.",
        "--prerelease", "--target", target)
    # The asset name must stay spx_web.zip: spxbench downloads it through a
    # fixed URL template component.


def delete_rc_release(rc_tag):
    log(f"7/7 deleting rc prerelease {rc_tag}")
    result = run("gh", "release", "delete", rc_tag, "--cleanup-tag", "--yes", check=False)
    if result.returncode != 0:
        log("rc deletion failed; ignore if creation never completed, otherwise delete manually")


class Validation:
    """One dry-run validation flow: shelf -> suite -> install -> run -> verdict.

    Steps 1 and 7 stay module-level so the cleanup scope covers failures of
    creation itself; steps 2-6 are methods sharing one constructed context.
    """

    def __init__(self):
        self.spxbench_url = (os.environ.get("SPXBENCH_URL") or DEFAULT_SPXBENCH_URL).strip().rstrip("/")
        self.token = require_env("SPXBENCH_API_TOKEN")
        self.zip_path = require_env("ZIP_PATH")
        self.rc_version = require_env("RC_VERSION")
        self.suite_name = os.environ.get("SUITE_NAME", "").strip() or "spx"
        self.timeout_minutes = int(os.environ.get("RUN_TIMEOUT_MINUTES", "").strip() or 30)
        self.rc_tag = f"v{self.rc_version}"
        self.deadline = time.monotonic() + self.timeout_minutes * 60

    def api(self, path, method="GET", payload=None):
        return api(f"{self.spxbench_url}{path}", self.token, method=method, payload=payload)

    def resolve_suite(self):
        # ids are per-database, names are the cross-repo contract.
        log(f"2/7 looking up suite {self.suite_name!r}")
        status, body = self.api("/api/suites")
        if status != 200:
            raise ValidationError(f"list suites failed: HTTP {status}: {body}")
        suite_id = find_suite_id(body.get("suites"), self.suite_name)
        if suite_id is None:
            available = ", ".join(sorted((s.get("name") or "") for s in body.get("suites") or []))
            raise ValidationError(f"no suite named {self.suite_name!r}; available: {available or '(none)'}")
        log(f"   suite id={suite_id}")
        return suite_id

    def register_candidate(self):
        # spxbench downloads the candidate from the rc shelf.
        asset_url = (f"https://github.com/{repository_name()}"
                     f"/releases/download/{self.rc_tag}/spx_web.zip")
        log(f"   candidate package: {asset_url}")
        log(f"3/7 registering version {self.rc_version} (server-side download takes ~15s)")
        status, body = self.api("/api/runtime-versions", method="POST",
                                payload={"action": "install", "kind": "spx",
                                         "version": self.rc_version, "sourceUrl": asset_url})
        if status != 200:
            raise ValidationError(f"install failed: HTTP {status}: {body}")
        digest = next((item.get("digest") for item in body.get("versions", [])
                       if item.get("kind") == "spx" and item.get("version") == self.rc_version), "")
        log(f"   installed, digest={digest}")

    def start_run(self, suite_id):
        # A busy slot (409) retries until deadline; permanent rejections fail now.
        log(f"4/7 starting suite run (suite={suite_id}, spxVersion={self.rc_version})")
        query = urllib.parse.urlencode({"suite": suite_id, "spxVersion": self.rc_version})
        job_id = None
        while job_id is None:
            status, body = self.api(f"/__e2e/api/run?{query}", method="POST")
            if status == 200:
                job_id = body.get("jobId")
            elif status == 409:
                if not is_busy_conflict(body):
                    detail = body if isinstance(body, str) else json.dumps(body)
                    raise ValidationError(f"run rejected: {detail.strip()}")
                log(f"   run slot busy (409), retrying in {BUSY_RETRY_SECONDS}s")
                if time.monotonic() >= self.deadline:
                    raise ValidationError("timed out waiting for a free run slot")
                time.sleep(BUSY_RETRY_SECONDS)
            else:
                raise ValidationError(f"run failed: HTTP {status}: {body}")
        log(f"   job #{job_id} accepted")
        return job_id

    def await_job(self, job_id):
        log(f"5/7 polling job state (every {POLL_INTERVAL_SECONDS}s)")
        while True:
            status, state = self.api(f"/__e2e/api/state?job={job_id}")
            if status != 200:
                raise ValidationError(f"state read failed: HTTP {status}: {state}")
            if not state.get("running") and state.get("jobId") == job_id:
                return state
            if time.monotonic() >= self.deadline:
                raise ValidationError(f"job #{job_id} did not finish within {self.timeout_minutes} minutes")
            log(f"   progress {state.get('completed')}/{state.get('total')} "
                f"passed={state.get('passed')} failed={state.get('failed')}")
            time.sleep(POLL_INTERVAL_SECONDS)

    def report(self, job_id, state):
        # The job's own terminal verdict decides; the case list only adds detail.
        total, passed, failed = state.get("total"), state.get("passed"), state.get("failed")
        log(f"6/7 result: summary={state.get('summary') or '(none)'} total={total} "
            f"passed={passed} failed={failed}")
        log(f"   details: {self.spxbench_url}/__e2e?job={job_id}")
        failures = failing_cases(state)
        if failures:
            log("   failing cases:")
            for case in failures:
                log(f"     - {case.get('name')} [{case.get('status')}]")
        problems = job_problems(state)
        if problems:
            raise ValidationError(f"candidate {self.rc_version} failed suite {self.suite_name!r}: "
                                  + "; ".join(problems))
        log(f"   all green: {self.rc_version} passed suite {self.suite_name!r} ({total} cases)")

    def run(self):
        try:
            # Creation is inside the try: gh release create is multi-step and can
            # fail after the remote release already exists; finally must clean it.
            create_rc_release(self.rc_tag, self.zip_path, commit_sha())
            suite_id = self.resolve_suite()
            self.register_candidate()
            job_id = self.start_run(suite_id)
            state = self.await_job(job_id)
            self.report(job_id, state)
        finally:
            delete_rc_release(self.rc_tag)


def main():
    Validation().run()


if __name__ == "__main__":
    try:
        main()
    except ValidationError as error:
        # Surface the failure as a GitHub annotation, like plan.py does.
        print(f"::error title=validate-e2e::{error}", flush=True)
        print(f"[error] {error}", file=sys.stderr, flush=True)
        raise SystemExit(1)