### Fixed
- **Grimmory "Test Connection" failures now show the real error** (#1431) — a failed test displayed the bare HTTP status text ("Bad Gateway") instead of the actual diagnostic (connection refused, login rejected, upstream proxy error). The UI now surfaces the full message, upstream HTTP errors are labeled with their status code, and failures are logged server-side.
