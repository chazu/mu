package plugin

import "fmt"

// ValidateRecordObserveResponse checks the observe envelope used by plugins
// whose output is a stream of records for downstream consumers such as pudl.
// Convergence-only plugins may use a different Current shape and should not
// call this validator.
//
// Successful responses contain current.records as an array of objects. Every
// record has a non-empty string _schema discriminator. Error responses are
// all-or-nothing: they contain error and no current payload.
func ValidateRecordObserveResponse(resp ObserveResponse) error {
	if resp.Error != "" {
		if resp.Current != nil {
			return fmt.Errorf("error response must not include current")
		}
		return nil
	}
	if resp.Current == nil {
		return fmt.Errorf("current is required")
	}

	rawRecords, ok := resp.Current["records"]
	if !ok {
		return fmt.Errorf("current.records is required")
	}
	records, ok := rawRecords.([]any)
	if !ok {
		return fmt.Errorf("current.records must be an array")
	}
	for i, rawRecord := range records {
		record, ok := rawRecord.(map[string]any)
		if !ok {
			return fmt.Errorf("current.records[%d] must be an object", i)
		}
		schema, ok := record["_schema"].(string)
		if !ok || schema == "" {
			return fmt.Errorf("current.records[%d]._schema must be a non-empty string", i)
		}
	}
	return nil
}
