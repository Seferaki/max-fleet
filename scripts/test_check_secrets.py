import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-secrets.py")
SPEC = importlib.util.spec_from_file_location("check_secrets", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC is not None and SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class SecretPathPolicyTests(unittest.TestCase):
    def test_data_service_source_is_allowed(self):
        self.assertIsNone(MODULE.FORBIDDEN_PATH.search("services/data/api/main.py"))

    def test_root_runtime_data_is_forbidden(self):
        self.assertIsNotNone(MODULE.FORBIDDEN_PATH.search("data/snapshot.json"))

    def test_secrets_and_backups_remain_forbidden(self):
        for path in ("services/data/secrets/token", "backups/state.dump"):
            with self.subTest(path=path):
                self.assertIsNotNone(MODULE.FORBIDDEN_PATH.search(path))


if __name__ == "__main__":
    unittest.main()
