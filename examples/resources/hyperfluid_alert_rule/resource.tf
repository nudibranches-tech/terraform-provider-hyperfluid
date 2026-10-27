# Alert when a Trino cluster has queries queued for ten minutes.
resource "hyperfluid_alert_rule" "trino_queue" {
  resource_kind  = "trino"
  resource_ref   = "00000000-0000-0000-0000-000000000000"
  resource_name  = "my-lake"
  panel_id       = "trino_queued_queries"
  warning_value  = 10
  critical_value = 25
  for_seconds    = 600
}
