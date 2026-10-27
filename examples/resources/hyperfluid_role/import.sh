# Role import id is the role id (uuid). System roles (owner, editor, viewer)
# cannot be imported: reference them with the hyperfluid_role data source.
terraform import hyperfluid_role.auditor 00000000-0000-0000-0000-000000000000
