# Import id is "<env>/<key_id>", or "<env>/<storage_zone_id>/<key_id>" for a key
# of another storage zone. The secret cannot be imported, and endpoint and region
# stay null.
terraform import hyperfluid_s3_access_key.etl 00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111
