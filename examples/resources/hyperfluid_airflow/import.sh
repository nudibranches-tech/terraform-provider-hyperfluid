# Airflow environment import id is the environment id (uuid).
# node_tier is never echoed by the API; it is recovered from the resolved
# cpu/memory, so it lands on import for any tier still in the catalogue.
terraform import hyperfluid_airflow.analytics 00000000-0000-0000-0000-000000000000
