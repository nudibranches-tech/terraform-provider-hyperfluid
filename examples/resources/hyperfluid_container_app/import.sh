# Container app import id is the app id (uuid).
# resource_tier is not returned by the API, so it cannot be recovered on import —
# set it in config afterward (cpu/memory remain available as computed attributes).
terraform import hyperfluid_container_app.web 00000000-0000-0000-0000-000000000000
