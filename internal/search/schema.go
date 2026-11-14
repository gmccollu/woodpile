package search

import "strconv"

// The data types Events can show, and each one's fields (GET /api/search/schema,
// SPEC §10.1.1 and §10.1.6). The client builds its field panel from this list
// alone, so a new data type adds an entry here with its own fields.

// TypeSyslog is the only data type in 1.1, and the default.
const TypeSyslog = "syslog"

// DomainValue is one value of a closed field.
type DomainValue struct {
	Value, Label string
}

// FieldSchema describes one field of a type's field panel.
type FieldSchema struct {
	Key, Label string
	// Param is the filter parameter a value goes into; ExcludeParam the one
	// that excludes it ("" for a field with a Domain: send the complement).
	Param, ExcludeParam string
	// Domain is the complete value set of a closed field, or nil.
	Domain []DomainValue
}

// TypeSchema is one data type.
type TypeSchema struct {
	Type, Label string
	Default     bool
	Fields      []FieldSchema
}

// Field keys of the syslog type, in panel order.
const (
	FieldDevice   = "device"
	FieldSeverity = "severity"
	FieldFacility = "facility"
	FieldHost     = "host"
	FieldApp      = "app"
)

var syslogFields = func() []FieldSchema {
	sev := make([]DomainValue, 0, 9)
	for i, n := range SeverityNames {
		sev = append(sev, DomainValue{strconv.Itoa(i), n})
	}
	sev = append(sev, DomainValue{"unknown", "unknown"})
	fac := make([]DomainValue, 0, 25)
	for i, n := range FacilityNames {
		fac = append(fac, DomainValue{strconv.Itoa(i), n})
	}
	fac = append(fac, DomainValue{"unknown", "unknown"})
	return []FieldSchema{
		{Key: FieldDevice, Label: "Device", Param: "devices", ExcludeParam: "exclude_devices"},
		{Key: FieldSeverity, Label: "Severity", Param: "severities", Domain: sev},
		{Key: FieldFacility, Label: "Facility", Param: "facilities", Domain: fac},
		{Key: FieldHost, Label: "Host", Param: "host", ExcludeParam: "exclude_host"},
		{Key: FieldApp, Label: "Program/tag", Param: "app", ExcludeParam: "exclude_app"},
	}
}()

// Schema returns every data type, the default first.
func Schema() []TypeSchema {
	return []TypeSchema{{Type: TypeSyslog, Label: "Syslog", Default: true, Fields: syslogFields}}
}

// CheckType validates a type parameter ("" is the default).
func CheckType(t string) error {
	if t == "" || t == TypeSyslog {
		return nil
	}
	return &QueryError{Msg: "type: unknown data type " + strconv.Quote(t) + "."}
}
