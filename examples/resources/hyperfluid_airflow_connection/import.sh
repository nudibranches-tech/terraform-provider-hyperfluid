# Airflow connection import id is "<airflow_id>/<conn_id>" — the conn_id a DAG
# asks for, not the connection object's own name. That name is resolved on the
# first read and reported as the computed `name` attribute.
terraform import hyperfluid_airflow_connection.warehouse 00000000-0000-0000-0000-000000000000/warehouse
