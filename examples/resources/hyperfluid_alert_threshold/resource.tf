# Warn earlier than the platform default on persistent volume usage.
# Destroying the resource returns the signal to the platform defaults.
resource "hyperfluid_alert_threshold" "pvc_disk" {
  signal       = "pvc_disk"
  warning_pct  = 70
  critical_pct = 85
}
