// Copyright IBM Corp. 2021, 2025
// SPDX-License-Identifier: MPL-2.0

package client

import "fmt"

// ServerError is what a 5xx from the console comes back as. It carries no
// information the plain error did not — the message is byte-for-byte the same —
// but it makes the class of the failure matchable, which is the one thing a
// caller cannot recover from a formatted string.
//
// The distinction that needs it is retryability. A wait that polls a resource
// for fifteen minutes sees three kinds of failure: an answer about the resource
// (ErrNotFound, ErrForbidden), a failure to ask it (a transport error, or one of
// these), and a verdict the poll's own logic reached about a phase it read. Only
// the middle kind is worth asking again about, and only the middle kind is
// distinguishable here rather than at the call site — hence this type, and not a
// string match on "-> 5" somewhere in the provider.
//
// A 4xx deliberately does not get one: a poll that issues the same GET it
// already issued does not turn a 400 into a 200, so retrying it would only delay
// the diagnostic the user needs.
type ServerError struct {
	Op     string
	Status int
	Body   string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("hyperfluid: %s -> %d: %s", e.Op, e.Status, e.Body)
}
