# Open data pipeline import id is the pipeline id, as for hyperfluid_pipeline, so a
# pipeline created from the console's Open Data page imports here. The connector, its
# params and the destination are recovered from the pipeline, and a configuration that
# does not match them plans a replacement. A pipeline that is not an open data connector
# pipeline is refused. `max_duration_minutes` is not recovered.
terraform import hyperfluid_open_data_pipeline.cat_opidor 00000000-0000-0000-0000-000000000000
