# API key import id is the key id (uuid).
# The secret `key` is returned only at creation and cannot be recovered, so it
# stays null after import (rotate the key to obtain a new secret if needed).
# `expires_in_days` is likewise not echoed by the API and stays null on import.
terraform import hyperfluid_ai_api_key.gateway 00000000-0000-0000-0000-000000000000
