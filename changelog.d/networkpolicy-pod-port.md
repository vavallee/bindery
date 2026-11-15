### Fixed
- **Helm NetworkPolicy with a custom `service.port`**. With `networkPolicy.enabled=true` and `service.port` set to anything other than 8787 (say 80), the policy opened the Service port on the pod, where nothing listens, so every connection to Bindery was dropped, including the PostSync smoke check. The policy now names the container port, so `service.port` can be whatever you like.
