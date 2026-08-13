"""Tests for runner.field_to_dict scope/guild_scoped normalization."""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))  # sdk/python

from custombot.runner import field_to_dict


class FieldToDictScopeTest(unittest.TestCase):
    def test_omitted_scope_with_guild_scoped_true(self):
        # guild_scoped=True and no scope must NOT silently become global.
        d = field_to_dict({"key": "k", "type": "text", "guild_scoped": True})
        self.assertEqual(d["scope"], "guild")
        self.assertTrue(d["guild_scoped"])

    def test_omitted_scope_with_guild_scoped_false(self):
        d = field_to_dict({"key": "k", "type": "text", "guild_scoped": False})
        self.assertEqual(d["scope"], "global")
        self.assertFalse(d["guild_scoped"])

    def test_omitted_guild_scoped_with_scope_guild(self):
        # scope="guild" and no guild_scoped must be derived as True.
        d = field_to_dict({"key": "k", "type": "text", "scope": "guild"})
        self.assertEqual(d["scope"], "guild")
        self.assertTrue(d["guild_scoped"])

    def test_omitted_guild_scoped_with_scope_global(self):
        d = field_to_dict({"key": "k", "type": "text", "scope": "global"})
        self.assertEqual(d["scope"], "global")
        self.assertFalse(d["guild_scoped"])

    def test_agreeing_values_pass(self):
        d = field_to_dict({"key": "k", "type": "toggle", "scope": "guild", "guild_scoped": True})
        self.assertEqual(d["scope"], "guild")
        self.assertTrue(d["guild_scoped"])

    def test_contradictory_values_rejected(self):
        for conflict in (
            {"key": "k", "scope": "guild", "guild_scoped": False},
            {"key": "k", "scope": "global", "guild_scoped": True},
        ):
            with self.assertRaises(ValueError):
                field_to_dict(conflict)

    def test_invalid_scope_rejected(self):
        with self.assertRaises(ValueError):
            field_to_dict({"key": "k", "scope": "server"})


if __name__ == "__main__":
    unittest.main()
