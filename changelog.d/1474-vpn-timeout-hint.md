### Fixed
- **VPN-killswitch timeouts are named, not just reported** (#1474) — when an
  ABS or Calibre-plugin "Test connection" times out against a LAN-shaped host
  (private IP, bare Docker hostname, `.local`/`.lan` suffix), the error now
  points at the usual culprit: Bindery sharing a VPN container's network
  (`network_mode: service:gluetun`) whose killswitch drops LAN traffic, with
  the `FIREWALL_OUTBOUND_SUBNETS` fix named inline. Real upstream errors
  (auth failures, refused connections) and public hosts keep their unmodified
  message. Complements the "Running Bindery behind a VPN" deployment docs.
