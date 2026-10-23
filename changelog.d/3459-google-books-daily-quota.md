### Fixed
- **Google Books daily quota refusals stop the day** (#3459). A 403 or 429 whose message names the `Queries per day` limit is now treated as the daily quota running out, whatever its reason field says. Google sends some of them with reason `rateLimitExceeded`, which was retried like a per minute throttle, so scheduled work kept asking. The per minute limit is still retried.
