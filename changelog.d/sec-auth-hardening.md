### Security
- **Logging out now ends that session on the server.** Before, logout only cleared the browser's cookie, so a copied cookie kept working until it expired. Your other devices stay signed in.
- **Login attempt limits hold under a burst.** Many simultaneous wrong passwords from one address could all be checked before the limit kicked in. The same fix covers OPDS reader logins. OPDS readers that send their password with every request are remembered for a few minutes after a successful check, so fetching many covers at once stays fast instead of tripping the attempt limit.
- **Password checks can no longer exhaust memory.** Only a few run at once, so a flood of login requests queues instead of running the server out of memory.
- **First run setup creates exactly one admin**, even if the setup form is submitted several times at once.
