package logstorage

import "testing"

func TestGetQueryAutocomplete(t *testing.T) {
	f := func(s string, cursor int, statusExpected ParseStatus, contextExpected ParseContext, reasonExpected ParseIncompleteReason, blockedExpected, errExpected bool) {
		t.Helper()

		ac := GetQueryAutocomplete(s, 0, cursor)
		if ac == nil {
			t.Fatalf("expecting non-nil autocomplete state")
		}
		if ac.Status != statusExpected {
			t.Fatalf("unexpected autocomplete status; got %q; want %q; err=%v; reason=%s", ac.Status, statusExpected, ac.Err, ac.Reason)
		}
		if ac.Context != contextExpected {
			t.Fatalf("unexpected autocomplete context; got %q; want %q", ac.Context, contextExpected)
		}
		if ac.Blocked != blockedExpected {
			t.Fatalf("unexpected blocked state; got %v; want %v; err=%v; reason=%d", ac.Blocked, blockedExpected, ac.Err, ac.Reason)
		}
		if ac.Reason != reasonExpected {
			t.Fatalf("unexpected incomplete reason; got %d; want %d", ac.Reason, reasonExpected)
		}
		if (ac.Err != nil) != errExpected {
			t.Fatalf("unexpected error state; got %v; want error=%v", ac.Err, errExpected)
		}
	}

	f(`*`, len(`*`), ParseStatusValid, ParseContextQuery, ParseIncompleteNone, false, false)
	f(`* |`, len(`* |`), ParseStatusIncompleteAtCursor, ParseContextPipe, ParseIncompleteMissingToken, false, true)
	f(`* | st`, len(`* | st`), ParseStatusValid, ParseContextPipeName, ParseIncompleteNone, false, false)
	f(`* | stats cou`, len(`* | stats cou`), ParseStatusIncompleteAtCursor, ParseContextStatsFunc, ParseIncompletePartialToken, false, true)
	f(`* | count(`, len(`* | count(`), ParseStatusIncompleteAtCursor, ParseContextField, ParseIncompleteMissingToken, false, true)
	f(`* | count("a", @)`, len(`* | count("a", `), ParseStatusIncompleteAtCursor, ParseContextField, ParseIncompleteMissingToken, false, true)
	f(`* | stat " | stats count()`, len(`* | stat " | stats count()`), ParseStatusInvalidCrossingCursor, ParseContextPipe, ParseIncompleteNone, true, true)

	s := `* | foo:bar | stats count(`
	f(s, len(`* | foo:bar`), ParseStatusInvalidAfterCursor, ParseContextPipe, ParseIncompleteNone, false, true)
}
