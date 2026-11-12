"""Unit tests for the release-candidate e2e validation helpers."""

import os
import unittest

from validate_e2e import ValidationError, failing_cases, find_suite_id, require_env


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


if __name__ == "__main__":
    unittest.main()
