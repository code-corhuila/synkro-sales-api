import unittest
from pathlib import Path

class DeploymentContract(unittest.TestCase):
    def test_sales_application_connection_is_separate(self):
        composition = (Path(__file__).resolve().parents[1] / 'deploy/compose.yml').read_text()
        self.assertIn('MONGODB_URI: ${SALES_APP_MONGODB_URI:?', composition)
        self.assertNotIn('MONGO_ADMIN', composition)
        self.assertNotIn('ports:', composition)

if __name__ == '__main__':
    unittest.main()