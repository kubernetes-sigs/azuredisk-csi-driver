package azureutils

import (
	"fmt"
	"net/http"
	"time"
)

var (
	// The function to get current time.
	now = time.Now

	// StatusCodesForRetry are a defined group of status code for which the client will retry.
	StatusCodesForRetry = []int{
		http.StatusRequestTimeout,      // 408
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
	}
)

// Error indicates an error returned by Azure APIs.
type Error struct {
	// Retriable indicates whether the request is retriable.
	Retriable bool
	// HTTPStatusCode indicates the response HTTP status code.
	HTTPStatusCode int
	// RetryAfter indicates the time when the request should retry after throttling.
	// A throttled request is retriable.
	RetryAfter time.Time
	// RetryAfter indicates the raw error from API.
	RawError error
}

// Error returns the error.
// Note that Error doesn't implement error interface because (nil *Error) != (nil error).
func (err *Error) Error() error {
	if err == nil {
		return nil
	}

	// Convert time to seconds for better logging.
	retryAfterSeconds := 0
	curTime := now()
	if err.RetryAfter.After(curTime) {
		retryAfterSeconds = int(err.RetryAfter.Sub(curTime) / time.Second)
	}

	return fmt.Errorf("Retriable: %v, RetryAfter: %ds, HTTPStatusCode: %d, RawError: %w",
		err.Retriable, retryAfterSeconds, err.HTTPStatusCode, err.RawError)
}
