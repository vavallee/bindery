### Removed
- **Custom format endpoints** (#1471). The `/api/v1/customformat` endpoints and the `custom_formats` table are gone. Search and grabbing never scored a release against a custom format and there was no UI for them, so formats created through the API never had any effect. The upgrade drops the table.
