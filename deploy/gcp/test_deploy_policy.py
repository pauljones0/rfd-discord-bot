import unittest
from deploy import validate_gemini


class FreeGeminiPolicyTests(unittest.TestCase):
    def config(self):
        return {'BOT_REQUEST_ROLE': 'worker', 'GEMINI_API_KEY': 'fixture-key',
                'GEMINI_MODELS': 'gemini-3.5-flash-lite',
                'GEMINI_FREE_PROJECT': 'rfd-gemini-free-fixture',
                'GEMINI_FREE_PROJECT_NUMBER': '123'}

    def test_free_key_is_accepted_and_paid_mismatched_or_command_keys_fail(self):
        free = {'projectId': 'rfd-gemini-free-fixture', 'billingEnabled': False, 'billingAccountName': ''}
        owned = {'parent': 'projects/123/locations/global', 'name': 'projects/123/locations/global/keys/fixture'}
        validate_gemini(self.config(), lambda _: free, lambda _: owned)
        for field, value in [('BOT_REQUEST_ROLE', 'commands'), ('GEMINI_API_KEY', 'one,two'),
                             ('GEMINI_MODELS', 'other-model'), ('GEMINI_FREE_PROJECT_NUMBER', '456')]:
            config = self.config()
            config[field] = value
            with self.assertRaises(AssertionError):
                validate_gemini(config, lambda _: free, lambda _: owned)
        for billing in [{}, {**free, 'billingEnabled': True}, {**free, 'billingAccountName': 'billingAccounts/paid'}]:
            with self.assertRaises(AssertionError):
                validate_gemini(self.config(), lambda _: billing, lambda _: owned)

    def test_disabled_ai_does_not_contact_provider_or_read_keys(self):
        def forbidden(_):
            self.fail('Disabled Gemini must not contact the provider')
        validate_gemini({'BOT_REQUEST_ROLE': 'worker'}, forbidden, forbidden)


if __name__ == '__main__':
    unittest.main()
