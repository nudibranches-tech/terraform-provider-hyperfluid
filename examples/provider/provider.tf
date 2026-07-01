# With hfctl brokering credentials, no provider config is required:
#
#   hfctl auth login --service-account ./service_account.json
#   hfctl tf auth        # writes the active profile's credential for Terraform
#   terraform plan       # provider discovers it automatically
#
provider "hyperfluid" {}

# Or point the provider at a service-account JSON explicitly (also settable via
# the HYPERFLUID_CREDENTIALS environment variable, e.g. in CI):
#
# provider "hyperfluid" {
#   endpoint         = "https://console.example.com" # or HYPERFLUID_ENDPOINT
#   credentials_file = "./service_account.json"      # or HYPERFLUID_CREDENTIALS
#   # organization_id is read from the credentials file unless overridden here.
# }
