### Security
- **Bounded HTTP metric labels** (#2954). Unauthenticated requests with made up methods or unknown paths could each create a new permanent Prometheus series, growing memory without limit. Unknown methods are now counted as `OTHER` and requests no route matched as `unmatched`.
