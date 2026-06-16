provider "hyperfluid" {
  # Console API base URL (or HYPERFLUID_ENDPOINT).
  endpoint = "https://console.example.com"

  # Path to the service-account JSON downloaded from the console
  # (the same file hfctl consumes). May also be set via HYPERFLUID_CREDENTIALS.
  # organization_id is read from this file unless overridden here.
  credentials_file = "~/.hyperfluid/sa.json"
}
