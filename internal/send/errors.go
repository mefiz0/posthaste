package send

import (
	"context"
	"errors"
	"net/textproto"
	"regexp"
	"strconv"

	gomail "github.com/wneessen/go-mail"
)

// Reply-line and enhanced status code shapes found inside SMTP transcript
// text. Servers (and libraries wrapping them) surface rejections in several
// layouts, so classification looks for both the three-digit reply code at the
// start of a line and the dotted enhanced form anywhere.
var (
	replyCodePattern    = regexp.MustCompile(`(?m)^[ \t]*(\d{3})[ \t-]`)
	enhancedCodePattern = regexp.MustCompile(`\b([45])\.\d{1,3}\.\d{1,3}\b`)
)

// Permanent reports whether a delivery error will still fail on every retry
// and must therefore move the message to the failed state immediately.
// Permanent SMTP rejections (5xx: invalid recipient, relay denied, rejected
// credentials), malformed messages, and everything typed as a *Rejection are
// permanent. Connection faults, timeouts, and 4xx replies are transient.
// Unknown errors are treated as transient: an unsent message is worth
// retrying, and the attempt budget bounds how long that can continue.
func Permanent(err error) bool {
	if err == nil {
		return false
	}
	// Cancellation is never a verdict about the message; a cancelled or
	// timed-out attempt simply did not complete.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrInvalidMessage) {
		return true
	}

	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Code >= 500
	}
	// go-mail reports most delivery failures as *SendError with the SMTP
	// reply code attached; read the code rather than its prose.
	var sendErr *gomail.SendError
	if errors.As(err, &sendErr) {
		if code := sendErr.ErrorCode(); code != 0 {
			return code >= 500
		}
		if !sendErr.IsTemp() {
			return true
		}
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code >= 500
	}
	return permanentByText(err.Error())
}

// permanentByText scans free-form error text for SMTP reply or enhanced
// status codes and reports whether they indicate a permanent rejection.
func permanentByText(text string) bool {
	if match := enhancedCodePattern.FindStringSubmatch(text); match != nil {
		return match[1] == "5"
	}
	for _, match := range replyCodePattern.FindAllStringSubmatch(text, -1) {
		code, convErr := strconv.Atoi(match[1])
		if convErr != nil {
			continue
		}
		if code >= 500 {
			return true
		}
	}
	return false
}
