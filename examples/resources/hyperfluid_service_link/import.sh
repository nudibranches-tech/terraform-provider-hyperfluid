# Service link import id is the composite "env/name", where name is the
# platform-derived "<consumer>-<target>".
# target_ports is a choice, not something the API reports back — it stays null on
# import, so set it in config afterward if the link was created with explicit
# ports. `ports` remains available as a computed attribute either way.
terraform import hyperfluid_service_link.web_to_db default/web-main
