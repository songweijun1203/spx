"""Unit tests for the release-candidate e2e validation helpers."""

import os
import unittest
from unittest import mock

import validate_e2e
from validate_e2e import (ValidationError, failing_cases, find_suite_id, is_busy_conflict,
                          job_problems, require_env)


class FindSuiteIdTest(unittest.TestCase):
    def test_matches_by_exact_name(self):
        suites = [{"id": 7, "name": "spx"}, {"id": 9, "name": "smoke"}]
        self.assertEqual(find_suite_id(suites, "spx"), 7)
        self.assertEqual(find_suite_id(suites, "smoke"), 9)

    def test_missing_name_returns_none(self):
        self.assertIsNone(find_suite_id([{"id": 1, "name": "spx"}], "ghost"))
        self.assertIsNone(find_suite_id(None, "spx"))
        self.assertIsNone(find_suite_id([], "spx"))


class FailingCasesTest(unittest.TestCase):
    def test_collects_non_pass_cases(self):
        state = {"cases": [{"name": "a", "status": "pass"},
                           {"name": "b", "status": "fail"},
                           {"name": "c", "status": "skip"}]}
        self.assertEqual([case["name"] for case in failing_cases(state)], ["b", "c"])

    def test_empty_state_has_no_failures(self):
        self.assertEqual(failing_cases({}), [])
        self.assertEqual(failing_cases({"cases": None}), [])


class RequireEnvTest(unittest.TestCase):
    def test_missing_variable_raises(self):
        os.environ.pop("VALIDATE_E2E_TEST_VAR", None)
        with self.assertRaises(ValidationError):
            require_env("VALIDATE_E2E_TEST_VAR")

    def test_blank_value_raises(self):
        os.environ["VALIDATE_E2E_TEST_VAR"] = "   "
        try:
            with self.assertRaises(ValidationError):
                require_env("VALIDATE_E2E_TEST_VAR")
        finally:
            os.environ.pop("VALIDATE_E2E_TEST_VAR", None)

    def test_value_is_trimmed(self):
        os.environ["VALIDATE_E2E_TEST_VAR"] = " x "
        try:
            self.assertEqual(require_env("VALIDATE_E2E_TEST_VAR"), "x")
        finally:
            os.environ.pop("VALIDATE_E2E_TEST_VAR", None)


class JobVerdictTest(unittest.TestCase):
    def test_all_cases_pass_but_job_verdict_failed_is_red(self):
        state = {"summary": "failed", "lastError": "runner crashed", "total": 2, "completed": 2,
                 "cases": [{"name": "a", "status": "pass"}, {"name": "b", "status": "pass"}]}
        problems = job_problems(state)
        self.assertTrue(any("summary" in p for p in problems))
        self.assertTrue(any("job error" in p for p in problems))

    def test_clean_success_has_no_problems(self):
        state = {"summary": "passed", "total": 2, "completed": 2,
                 "cases": [{"name": "a", "status": "pass"}, {"name": "b", "status": "pass"}]}
        self.assertEqual(job_problems(state), [])

    def test_incomplete_job_is_red(self):
        state = {"summary": "passed", "total": 5, "completed": 4,
                 "cases": [{"name": "a", "status": "pass"}]}
        self.assertTrue(any("incomplete" in p for p in job_problems(state)))

    def test_failed_case_list_is_reported(self):
        state = {"summary": "passed", "total": 2, "completed": 2,
                 "cases": [{"name": "a", "status": "pass"}, {"name": "b", "status": "fail"}]}
        self.assertTrue(any("did not pass" in p for p in job_problems(state)))


class RunConflictTest(unittest.TestCase):
    def test_busy_conflict_is_recoverable(self):
        self.assertTrue(is_busy_conflict("a test run is already in progress\n"))

    def test_permanent_rejection_is_not_busy(self):
        self.assertFalse(is_busy_conflict("suite 1 selects no ready test cases\n"))
        self.assertFalse(is_busy_conflict({"error": "boom"}))


class CleanupScopeTest(unittest.TestCase):
    def test_creation_failure_still_cleans_up(self):
        env = {"SPXBENCH_URL": "http://127.0.0.1", "SPXBENCH_API_TOKEN": "t",
               "ZIP_PATH": "dist/spx_web.zip", "RC_VERSION": "0.0.1-rc.1.1"}
        with mock.patch.dict(os.environ, env), \
             mock.patch.object(validate_e2e, "create_rc_release",
                               side_effect=ValidationError("boom")), \
             mock.patch.object(validate_e2e, "delete_rc_release") as deleted:
            with self.assertRaises(ValidationError):
                validate_e2e.main()
            deleted.assert_called_once_with("v0.0.1-rc.1.1")


if __name__ == "__main__":
    unittest.main()
