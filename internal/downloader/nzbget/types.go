package nzbget

// rpcRequest is the JSON-RPC request envelope.
type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

// versionResponse is the result of calling the "version" method.
type versionResponse struct {
	Result string `json:"result"`
}

// appendResponse is the result of calling the "append" method.
// NZBGet returns the NZBID (positive integer) on success, or 0 on failure.
type appendResponse struct {
	Result int `json:"result"`
}

// editQueueResponse is the result of calling "editqueue".
type editQueueResponse struct {
	Result bool `json:"result"`
}

// Group represents an active download in NZBGet's queue (listgroups).
type Group struct {
	NZBID            int     `json:"NZBID"`
	NZBName          string  `json:"NZBName"`
	Status           string  `json:"Status"`
	Category         string  `json:"Category"`
	FileSizeMB       float64 `json:"FileSizeMB"`
	RemainingSizeMB  float64 `json:"RemainingSizeMB"`
	DownloadedSizeMB float64 `json:"DownloadedSizeMB"`
	ActiveDownloads  int     `json:"ActiveDownloads"`
	URL              string  `json:"URL"`
}

// listGroupsResponse is the result of calling "listgroups".
type listGroupsResponse struct {
	Result []Group `json:"result"`
}

// HistoryItem represents a completed/failed download in NZBGet's history.
type HistoryItem struct {
	NZBID      int     `json:"NZBID"`
	NZBName    string  `json:"NZBName"`
	Status     string  `json:"Status"`
	Category   string  `json:"Category"`
	FileSizeMB float64 `json:"FileSizeMB"`
	DestDir    string  `json:"DestDir"`
	URL        string  `json:"URL"`
}

// historyResponse is the result of calling "history".
type historyResponse struct {
	Result []HistoryItem `json:"result"`
}

// configEntry is a single name/value pair from NZBGet's "config" RPC. Both
// fields come through as strings regardless of the underlying option type.
type configEntry struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

// configResponse is the result of calling "config", a flat list of name/value
// pairs covering every option in NZBGet's nzbget.conf. Categories show up as
// CategoryN.Name=Books, CategoryN.DestDir=…, CategoryN.Unpack=yes, etc.
type configResponse struct {
	Result []configEntry `json:"result"`
}
