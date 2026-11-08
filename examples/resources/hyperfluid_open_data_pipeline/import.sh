# Open data pipeline import id is the pipeline id, as for hyperfluid_pipeline, so a
# pipeline created from the console's Open Data page imports here. `connector_slug`,
# `params`, `data_container_id`, `schema_name`, `table_name` and `max_duration_minutes`
# are not recovered: the first apply after the import records them from the configuration.
terraform import hyperfluid_open_data_pipeline.cat_opidor 00000000-0000-0000-0000-000000000000
