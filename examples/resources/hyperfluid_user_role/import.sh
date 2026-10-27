# Import id is "<user_id>/<role_id>" for an organization-wide role, or
# "<user_id>/<role_id>/<scope_path>" (the scope path may contain slashes).
# resource_type cannot be read back and stays null after import.
terraform import hyperfluid_user_role.alice_viewer 00000000-0000-0000-0000-000000000000/11111111-1111-1111-1111-111111111111
