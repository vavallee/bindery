### Security
- **Login rate limiter no longer grows without bound** (#2137). The per-address bucket map only expired entries for an address that came back, so a caller rotating source addresses could grow it indefinitely. Buckets are now swept once per window.
- **Telemetry server compares its stats token in constant time** (#2138). The `/api/stats` and `/api/backup` bearer check used a plain string comparison, whose timing leaks how much of a guess was correct.
