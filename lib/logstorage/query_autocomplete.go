package logstorage

// QueryAutocomplete contains parser state for query autocomplete at cursorPosition.
type QueryAutocomplete struct {
	Err     error
	Blocked bool
	Status  ParseStatus
	Context ParseContext
	Reason  ParseIncompleteReason
}

// GetQueryAutocomplete returns parser state needed for query autocomplete at cursorPosition.
func GetQueryAutocomplete(s string, timestamp int64, cursorPosition int) *QueryAutocomplete {
	result := parseQueryResult(s, timestamp, cursorPosition)

	return &QueryAutocomplete{
		Err:     result.Err,
		Blocked: isQueryAutocompleteBlocked(result.Status),
		Status:  result.Status,
		Context: result.Context,
		Reason:  result.Reason,
	}
}

func isQueryAutocompleteBlocked(status ParseStatus) bool {
	switch status {
	case ParseStatusValid, ParseStatusIncompleteAtCursor, ParseStatusInvalidAfterCursor:
		return false
	default:
		return true
	}
}
