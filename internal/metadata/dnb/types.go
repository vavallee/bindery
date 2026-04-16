package dnb

import "encoding/xml"

// sruResponse is the top-level SRU searchRetrieveResponse document.
type sruResponse struct {
	XMLName         xml.Name   `xml:"searchRetrieveResponse"`
	NumberOfRecords int        `xml:"numberOfRecords"`
	Records         sruRecords `xml:"records"`
}

type sruRecords struct {
	Records []sruRecord `xml:"record"`
}

type sruRecord struct {
	RecordData sruRecordData `xml:"recordData"`
}

type sruRecordData struct {
	MARCRecord marcRecord `xml:"record"`
}

// marcRecord is a single MARC21-XML record.
type marcRecord struct {
	XMLName       xml.Name           `xml:"record"`
	ControlFields []marcControlField `xml:"controlfield"`
	DataFields    []marcDataField    `xml:"datafield"`
}

type marcControlField struct {
	Tag   string `xml:"tag,attr"`
	Value string `xml:",chardata"`
}

type marcDataField struct {
	Tag       string         `xml:"tag,attr"`
	Subfields []marcSubfield `xml:"subfield"`
}

type marcSubfield struct {
	Code  string `xml:"code,attr"`
	Value string `xml:",chardata"`
}

// controlField returns the value of the first controlfield with the given tag.
func (r marcRecord) controlField(tag string) string {
	for _, cf := range r.ControlFields {
		if cf.Tag == tag {
			return cf.Value
		}
	}
	return ""
}

// subfield returns the first $<code> value in the first datafield with the given tag.
func (r marcRecord) subfield(tag, code string) string {
	for _, df := range r.DataFields {
		if df.Tag != tag {
			continue
		}
		for _, sf := range df.Subfields {
			if sf.Code == code {
				return sf.Value
			}
		}
	}
	return ""
}
