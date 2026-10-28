package api

// SystemFeatures is the "features" object in GET /system/status: optional API
// behaviour an integration can detect before relying on it, so it can fall
// back cleanly on an older Bindery instead of guessing from the version.
type SystemFeatures struct {
	// RequestsForUser means POST /requests accepts forUser from an admin
	// session or the API key, and stores the request as that account's
	// (#3337). SeerrNG reads it before sending the field.
	RequestsForUser bool `json:"requestsForUser"`
}

// CurrentSystemFeatures is what this build supports.
func CurrentSystemFeatures() SystemFeatures {
	return SystemFeatures{RequestsForUser: true}
}
